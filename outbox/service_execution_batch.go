package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	batchCollectorClaimLimit = 1
	batchFillPollInterval    = 2 * time.Millisecond
	batchFillClaimHeadroom   = 5 * time.Millisecond
	batchRetryBase           = 100 * time.Millisecond
	batchRetryMaximum        = 30 * time.Second
	batchDLQInsertAllowance  = 25 * time.Millisecond
	batchFinalizationMargin  = time.Second
)

func (s *Service) processOneAvailableBatch(
	ctx context.Context,
	log logger.Logger,
	capabilities []JobCapability,
	cursor *int,
) (bool, error) {
	if len(capabilities) == 0 {
		return false, nil
	}
	start := *cursor % len(capabilities)
	for offset := range capabilities {
		index := (start + offset) % len(capabilities)
		capability := capabilities[index]
		if s.batchCapabilityPaused(capability, time.Now().UTC()) {
			continue
		}
		processed, err := s.findAndProcessExecutionBatch(ctx, log, capability)
		if err != nil || processed {
			if processed {
				*cursor = (index + 1) % len(capabilities)
			}
			return processed, err
		}
	}
	return false, nil
}

func (s *Service) findAndProcessExecutionBatch(
	ctx context.Context,
	log logger.Logger,
	capability JobCapability,
) (bool, error) {
	s.mu.RLock()
	registration, ok := s.batchJobs[capability]
	s.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("batch capability disappeared: %s schema version %d", capability.Name, capability.SchemaVersion)
	}
	repo, ok := s.jobsRepo.(BatchJobsRepository)
	if !ok {
		return false, ErrBatchRepositoryNotConfigured
	}

	jobs, leaseToken, err := s.claimInitialExecutionBatch(ctx, repo, capability, registration.config)
	if errors.Is(err, ErrNoJobs) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	batchCtx, cancelBatch := context.WithCancelCause(ctx)
	defer cancelBatch(nil)
	manager := newBatchLeaseManager(batchCtx, s.jobsRepo, jobs, leaseToken, s.reserveFor, cancelBatch)
	filled, fillErr := s.fillExecutionBatch(
		batchCtx,
		repo,
		manager,
		capability,
		registration.config,
		jobs,
	)
	if fillErr != nil {
		heartbeatErr := manager.stopAndWait()
		return true, errors.Join(fillErr, heartbeatErr, manager.releaseUnstarted(ctx))
	}
	selected := append([]models.Job(nil), filled...)
	sortExecutionBatchJobs(selected)
	if err := manager.admit(batchCtx, s.drain); err != nil {
		heartbeatErr := manager.stopAndWait()
		return true, errors.Join(err, heartbeatErr, manager.releaseUnstarted(ctx))
	}

	items := make([]BatchJobItem, len(selected))
	for index, job := range selected {
		items[index] = BatchJobItem{JobID: job.ID, Payload: job.Payload, Attempt: job.Attempts}
	}
	handlerItems := append([]BatchJobItem(nil), items...)
	result, handleErr := s.executeBatchHandler(batchCtx, registration.job, handlerItems)
	if cause := context.Cause(batchCtx); cause != nil {
		manager.forgetAll()
		heartbeatErr := manager.stopAndWait()
		return true, errors.Join(cause, heartbeatErr)
	}
	var panicErr *HandlerPanicError
	if errors.As(handleErr, &panicErr) {
		manager.forgetAll()
		heartbeatErr := manager.stopAndWait()
		log.ErrorContext(ctx, "batch handler panicked",
			logger.Error(handleErr),
			slog.String("job_name", capability.Name),
			slog.Int64("schema_version", int64(capability.SchemaVersion)),
			slog.Int("batch_size", len(selected)),
			slog.String("stack", string(panicErr.Stack)),
		)
		return true, errors.Join(handleErr, heartbeatErr)
	}

	processErr := s.finishExecutionBatch(ctx, log, finishExecutionBatchInput{
		repo: repo, manager: manager, capability: capability, handler: registration.job,
		jobs: selected, items: items, result: result, handleErr: handleErr,
	})
	if processErr != nil {
		// Every managed row has already entered HandleBatch. On an invalid
		// result or failed finalization, leave those leases for expiry-based
		// recovery instead of compensating them as unstarted work.
		manager.forgetAll()
	}
	heartbeatErr := manager.stopAndWait()
	if processErr == nil && heartbeatErr != nil {
		processErr = heartbeatErr
	}
	releaseErr := manager.releaseUnstarted(ctx)

	return true, errors.Join(processErr, heartbeatErr, releaseErr)
}

func sortExecutionBatchJobs(jobs []models.Job) {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].AvailableAt.Equal(jobs[j].AvailableAt) {
			return jobs[i].AvailableAt.Before(jobs[j].AvailableAt)
		}
		if !jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
		}
		return jobs[i].ID.String() < jobs[j].ID.String()
	})
}

func (s *Service) claimInitialExecutionBatch(
	ctx context.Context,
	repo BatchJobsRepository,
	capability JobCapability,
	config normalizedBatchConfig,
) ([]models.Job, LeaseToken, error) {
	leaseToken := types.NewLeaseToken()
	boundedRepo, ok := repo.(BoundedBatchJobsRepository)
	if !ok {
		jobs, err := s.claimExecutionBatchSingletonWithToken(
			ctx,
			repo,
			capability,
			leaseToken,
		)
		return jobs, leaseToken, err
	}
	jobs, err := s.claimExecutionBatchBoundedWithToken(
		ctx,
		boundedRepo,
		capability,
		BatchClaimLimits{MaxMessages: config.maxMessages, MaxBytes: config.maxBytes},
		leaseToken,
	)
	return jobs, leaseToken, err
}

func (s *Service) claimExecutionBatchSingletonWithToken(
	ctx context.Context,
	repo BatchJobsRepository,
	capability JobCapability,
	leaseToken LeaseToken,
) ([]models.Job, error) {
	jobs, err := s.claimJobsWithLease(ctx, leaseToken, func(claimCtx context.Context, now, until time.Time) ([]models.Job, error) {
		return repo.FindAndReserveJobsForCapability(claimCtx, now, until, leaseToken, capability, batchCollectorClaimLimit)
	})
	if err != nil {
		return nil, fmt.Errorf("find and reserve execution batch: %w", err)
	}
	if err := validateClaimedExecutionBatch(jobs, capability, leaseToken, batchCollectorClaimLimit); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Service) claimExecutionBatchBoundedWithToken(
	ctx context.Context,
	repo BoundedBatchJobsRepository,
	capability JobCapability,
	limits BatchClaimLimits,
	leaseToken LeaseToken,
) ([]models.Job, error) {
	jobs, err := s.claimJobsWithLease(ctx, leaseToken, func(claimCtx context.Context, now, until time.Time) ([]models.Job, error) {
		return repo.FindAndReserveJobsForCapabilityBounded(claimCtx, now, until, leaseToken, capability, limits)
	})
	if err != nil {
		return nil, fmt.Errorf("find and reserve execution batch: %w", err)
	}
	if err := validateClaimedExecutionBatch(jobs, capability, leaseToken, limits.MaxMessages); err != nil {
		return nil, err
	}
	if err := validateBoundedExecutionBatchPayload(jobs, limits.MaxBytes); err != nil {
		return nil, err
	}
	return jobs, nil
}

func validateBoundedExecutionBatchPayload(jobs []models.Job, maxBytes int) error {
	usedBytes := 0
	for index, job := range jobs {
		payloadBytes := len(job.Payload)
		if index == 0 && payloadBytes > maxBytes {
			if len(jobs) == 1 {
				return nil
			}
			return fmt.Errorf(
				"%w: bounded batch returned %d jobs after oversized first payload",
				ErrUnsupportedClaim,
				len(jobs),
			)
		}
		if payloadBytes > maxBytes-usedBytes {
			return fmt.Errorf(
				"%w: bounded batch payload exceeds %d bytes at job %s",
				ErrUnsupportedClaim,
				maxBytes,
				job.ID,
			)
		}
		usedBytes += payloadBytes
	}
	return nil
}

func validateClaimedExecutionBatch(
	jobs []models.Job,
	capability JobCapability,
	leaseToken LeaseToken,
	limit int,
) error {
	if len(jobs) == 0 {
		return ErrEmptyReservationBatch
	}
	if len(jobs) > limit {
		return fmt.Errorf("batch repository returned %d jobs for limit %d", len(jobs), limit)
	}
	seen := make(map[types.JobID]struct{}, len(jobs))
	for _, job := range jobs {
		claimedCapability := JobCapability{
			Name:          job.Name,
			SchemaVersion: normalizeSchemaVersion(job.SchemaVersion),
		}
		if claimedCapability != capability {
			return fmt.Errorf(
				"%w: claimed %s schema version %d for %s schema version %d",
				ErrUnsupportedClaim,
				claimedCapability.Name,
				claimedCapability.SchemaVersion,
				capability.Name,
				capability.SchemaVersion,
			)
		}
		if err := validateClaimedJobLease(job, leaseToken, time.Now().UTC()); err != nil {
			return err
		}
		if _, duplicate := seen[job.ID]; duplicate {
			return fmt.Errorf("%w: duplicate claimed job %s", ErrLeaseLost, job.ID)
		}
		seen[job.ID] = struct{}{}
	}
	return nil
}

func (s *Service) releaseClaimedBatchTail(ctx context.Context, token LeaseToken, jobs []models.Job) error {
	if len(jobs) == 0 {
		return nil
	}
	ids := make([]types.JobID, len(jobs))
	for index := range jobs {
		ids[index] = jobs[index].ID
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()
	affected, err := s.jobsRepo.ReleaseUnstartedJobsWithLease(finalizeCtx, ids, token, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("release unstarted claimed jobs: %w", err)
	}
	if affected != int64(len(ids)) {
		return fmt.Errorf("%w: released %d of %d unstarted claimed jobs", ErrLeaseLost, affected, len(ids))
	}
	return nil
}

func (s *Service) fillExecutionBatch(
	ctx context.Context,
	repo BatchJobsRepository,
	manager *batchLeaseManager,
	capability JobCapability,
	config normalizedBatchConfig,
	initial []models.Job,
) ([]models.Job, error) {
	jobs := append([]models.Job(nil), initial...)
	usedBytes := 0
	for _, job := range jobs {
		usedBytes += len(job.Payload)
	}
	if len(jobs) >= config.maxMessages || usedBytes >= config.maxBytes {
		return jobs, nil
	}

	deadline := time.Now().Add(config.maxWait)
	for len(jobs) < config.maxMessages && usedBytes < config.maxBytes {
		if time.Until(deadline) <= 0 {
			break
		}

		claimed, fillDeadlineReached, err := s.claimExecutionBatchWithinFillWindow(
			ctx,
			repo,
			capability,
			manager.leaseToken,
			BatchClaimLimits{
				MaxMessages: config.maxMessages - len(jobs),
				MaxBytes:    config.maxBytes - usedBytes,
			},
			deadline,
		)
		if fillDeadlineReached {
			break
		}
		if errors.Is(err, ErrNoJobs) {
			remainingWait := time.Until(deadline)
			if remainingWait <= 0 {
				break
			}
			timer := time.NewTimer(min(remainingWait, batchFillPollInterval))
			select {
			case <-ctx.Done():
				timer.Stop()
				return jobs, context.Cause(ctx)
			case <-s.drain:
				timer.Stop()
				return jobs, ErrServiceDraining
			case <-timer.C:
			}
			continue
		}
		if err != nil {
			return jobs, fmt.Errorf("fill execution batch: %w", err)
		}
		selected, tail := selectBatchPayloadLimitWithUsed(claimed, config.maxBytes, usedBytes)
		if err := manager.add(selected); err != nil {
			return jobs, err
		}
		if err := s.releaseClaimedBatchTail(ctx, manager.leaseToken, tail); err != nil {
			return jobs, err
		}
		jobs = append(jobs, selected...)
		for _, job := range selected {
			usedBytes += len(job.Payload)
		}
		if len(tail) > 0 {
			break
		}
	}
	return jobs, nil
}

func (s *Service) claimExecutionBatchWithinFillWindow(
	ctx context.Context,
	repo BatchJobsRepository,
	capability JobCapability,
	leaseToken LeaseToken,
	limits BatchClaimLimits,
	deadline time.Time,
) ([]models.Job, bool, error) {
	// Leave a small minimum claim budget before the fill deadline. Starting
	// another repository operation with less time turns ordinary query and
	// scheduler latency into an avoidable cancellation. Some drivers must then
	// discard the underlying connection before it can return to the pool.
	if time.Until(deadline) <= batchFillClaimHeadroom {
		return nil, true, nil
	}

	claimCtx, cancelClaim := context.WithDeadline(ctx, deadline)
	jobs, err := s.claimExecutionBatchForFill(
		claimCtx,
		repo,
		capability,
		leaseToken,
		limits,
	)
	claimCause := context.Cause(claimCtx)
	cancelClaim()
	if err == nil {
		return jobs, false, nil
	}
	if errors.Is(err, errCancelledClaimCleanup) {
		// A normal fill timeout must not hide a failed release of rows that
		// the repository confirmed after cancellation.
		return nil, false, err
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, false, cause
	}
	// Drivers do not have to wrap the context error. PostgreSQL, for example,
	// reports a deadline-triggered CancelRequest as SQLSTATE 57014. Once our
	// private fill deadline has fired (and the parent is still alive), the
	// repository error belongs to that normal early flush.
	if errors.Is(claimCause, context.DeadlineExceeded) {
		return nil, true, nil
	}
	return nil, false, err
}

func (s *Service) claimExecutionBatchForFill(
	ctx context.Context,
	repo BatchJobsRepository,
	capability JobCapability,
	leaseToken LeaseToken,
	limits BatchClaimLimits,
) ([]models.Job, error) {
	boundedRepo, ok := repo.(BoundedBatchJobsRepository)
	if !ok {
		return s.claimExecutionBatchSingletonWithToken(
			ctx,
			repo,
			capability,
			leaseToken,
		)
	}
	return s.claimExecutionBatchBoundedWithToken(
		ctx,
		boundedRepo,
		capability,
		limits,
		leaseToken,
	)
}

func selectBatchPayloadLimitWithUsed(jobs []models.Job, maxBytes, used int) (selected, tail []models.Job) {
	selected = make([]models.Job, 0, len(jobs))
	for index, job := range jobs {
		if used+len(job.Payload) > maxBytes {
			return selected, jobs[index:]
		}
		selected = append(selected, job)
		used += len(job.Payload)
	}
	return selected, nil
}

func (s *Service) executeBatchHandler(
	ctx context.Context,
	job BatchJob,
	items []BatchJobItem,
) (result BatchResult, err error) {
	handlerCtx, cancel := context.WithTimeout(ctx, job.ExecutionTimeout())
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = BatchResult{}
			err = &HandlerPanicError{
				JobName: job.Name(),
				Value:   recovered,
				Stack:   debug.Stack(),
			}
		}
	}()
	result, err = job.HandleBatch(handlerCtx, items)
	if cause := context.Cause(handlerCtx); cause != nil {
		return BatchResult{}, cause
	}
	return result, err
}

type finishExecutionBatchInput struct {
	repo       BatchJobsRepository
	manager    *batchLeaseManager
	capability JobCapability
	handler    BatchJob
	jobs       []models.Job
	items      []BatchJobItem
	result     BatchResult
	handleErr  error
}

func (s *Service) finishExecutionBatch(
	ctx context.Context,
	log logger.Logger,
	input finishExecutionBatchInput,
) error {
	if input.handleErr != nil {
		var panicErr *HandlerPanicError
		if errors.As(input.handleErr, &panicErr) {
			return input.handleErr
		}
		if len(input.result.Items) != 0 {
			return errors.Join(
				fmt.Errorf("%w: non-empty result with top-level error", ErrInvalidBatchResult),
				input.handleErr,
			)
		}
		if IsPermanent(input.handleErr) {
			return fmt.Errorf("permanent top-level batch failure: %w", input.handleErr)
		}
		availableAt := s.topLevelBatchRetryTime(input.capability, input.handleErr)
		log.ErrorContext(ctx, "handle batch job error",
			logger.Error(input.handleErr),
			slog.String("job_name", input.capability.Name),
			slog.Int64("schema_version", int64(input.capability.SchemaVersion)),
			slog.Int("batch_size", len(input.jobs)),
			slog.Time("available_at", availableAt),
		)
		outcomes := make([]BatchJobOutcome, len(input.jobs))
		for index := range input.jobs {
			outcomes[index] = BatchJobOutcome{
				JobID:       input.jobs[index].ID,
				Kind:        BatchJobOutcomeDefer,
				AvailableAt: availableAt,
			}
		}
		return s.applyExecutionBatchOutcomes(ctx, input.repo, input.manager, input.jobs, outcomes)
	}

	errs, err := validateBatchResult(input.items, input.result)
	if err != nil {
		return err
	}
	s.resetBatchRetryStreak(input.capability)
	now := time.Now().UTC()
	outcomes := make([]BatchJobOutcome, len(input.jobs))
	for index, itemErr := range errs {
		outcome := BatchJobOutcome{JobID: input.jobs[index].ID}
		switch {
		case itemErr == nil:
			outcome.Kind = BatchJobOutcomeSuccess
		case IsPermanent(itemErr):
			outcome = dlqExecutionBatchOutcome(input.jobs[index], fmt.Sprintf("permanent failure: %v", itemErr))
		case hasDeferTime(itemErr):
			outcome.Kind = BatchJobOutcomeDefer
			outcome.AvailableAt, _ = DeferTime(itemErr)
			if outcome.AvailableAt.Before(now) {
				outcome.AvailableAt = now
			}
			s.pauseBatchCapability(input.capability, outcome.AvailableAt)
		case input.jobs[index].Attempts >= input.handler.MaxAttempts():
			outcome = dlqExecutionBatchOutcome(input.jobs[index], fmt.Sprintf("max attempts exceeded: %v", itemErr))
		default:
			outcome.Kind = BatchJobOutcomeRetry
			if retryAt, explicit := RetryTime(itemErr); explicit {
				outcome.AvailableAt = retryAt.UTC()
			} else {
				outcome.AvailableAt = now.Add(batchAttemptBackoff(input.jobs[index].Attempts))
			}
			if outcome.AvailableAt.Before(now) {
				outcome.AvailableAt = now
			}
		}
		outcomes[index] = outcome
		if itemErr != nil {
			log.ErrorContext(ctx, "handle batch job item error",
				logger.Error(itemErr),
				slog.String("job_name", input.jobs[index].Name),
				slog.String("job_id", input.jobs[index].ID.String()),
				slog.Int("attempt_number", input.jobs[index].Attempts),
			)
		}
	}
	return s.applyExecutionBatchOutcomes(ctx, input.repo, input.manager, input.jobs, outcomes)
}

func hasDeferTime(err error) bool {
	_, ok := DeferTime(err)
	return ok
}

func dlqExecutionBatchOutcome(job models.Job, reason string) BatchJobOutcome {
	return BatchJobOutcome{
		JobID:  job.ID,
		Kind:   BatchJobOutcomeDLQ,
		Reason: reason,
	}
}

func (s *Service) applyExecutionBatchOutcomes(
	ctx context.Context,
	repo BatchJobsRepository,
	manager *batchLeaseManager,
	jobs []models.Job,
	outcomes []BatchJobOutcome,
) error {
	finalizationTimeout := executionBatchFinalizationTimeout(outcomes)
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer cancel()
	deadline, _ := finalizeCtx.Deadline()
	return manager.finalizeAll(finalizeCtx, deadline.Add(batchFinalizationMargin), func() error {
		return s.transactor.RunInTx(finalizeCtx, func(txCtx context.Context) error {
			if err := s.createExecutionBatchDLQRecords(txCtx, jobs, outcomes); err != nil {
				return err
			}
			affected, err := repo.ApplyBatchJobOutcomes(
				txCtx,
				manager.leaseToken,
				time.Now().UTC(),
				outcomes,
			)
			if err != nil {
				return fmt.Errorf("apply batch job outcomes: %w", err)
			}
			if affected != int64(len(outcomes)) {
				return fmt.Errorf("%w: finalized %d of %d batch jobs", ErrLeaseLost, affected, len(outcomes))
			}
			return nil
		})
	})
}

func executionBatchFinalizationTimeout(outcomes []BatchJobOutcome) time.Duration {
	timeout := leaseFinalizationTimeout
	for _, outcome := range outcomes {
		if outcome.Kind == BatchJobOutcomeDLQ {
			timeout += batchDLQInsertAllowance
		}
	}
	return timeout
}

func (s *Service) createExecutionBatchDLQRecords(
	ctx context.Context,
	jobs []models.Job,
	outcomes []BatchJobOutcome,
) error {
	jobsByID := make(map[types.JobID]models.Job, len(jobs))
	for _, job := range jobs {
		jobsByID[job.ID] = job
	}
	for _, outcome := range outcomes {
		if outcome.Kind != BatchJobOutcomeDLQ {
			continue
		}
		job, ok := jobsByID[outcome.JobID]
		if !ok {
			return fmt.Errorf("%w: DLQ outcome references unknown job %s", ErrInvalidBatchResult, outcome.JobID)
		}
		if _, err := s.jobsFailedRepo.CreateFailedJobVersioned(
			ctx,
			job.ID,
			job.Name,
			job.SchemaVersion,
			job.Payload,
			outcome.Reason,
		); err != nil {
			return fmt.Errorf("create versioned failed batch job: %w", err)
		}
	}
	return nil
}

func batchAttemptBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := batchRetryBase
	for step := 1; step < attempt && delay < batchRetryMaximum; step++ {
		if delay > batchRetryMaximum/2 {
			return batchRetryMaximum
		}
		delay *= 2
	}
	return min(delay, batchRetryMaximum)
}

func (s *Service) topLevelBatchRetryTime(capability JobCapability, err error) time.Time {
	now := time.Now().UTC()
	if deferAt, ok := DeferTime(err); ok {
		if deferAt.Before(now) {
			deferAt = now
		}
		s.pauseBatchCapability(capability, deferAt)
		return deferAt
	}
	if retryAt, ok := RetryTime(err); ok {
		if retryAt.Before(now) {
			retryAt = now
		}
		s.pauseBatchCapability(capability, retryAt)
		return retryAt
	}

	s.batchStateMu.Lock()
	s.batchStreak[capability]++
	delay := batchAttemptBackoff(s.batchStreak[capability])
	availableAt := now.Add(delay)
	if availableAt.After(s.batchPaused[capability]) {
		s.batchPaused[capability] = availableAt
	}
	s.batchStateMu.Unlock()
	return availableAt
}

func (s *Service) resetBatchRetryStreak(capability JobCapability) {
	s.batchStateMu.Lock()
	delete(s.batchStreak, capability)
	s.batchStateMu.Unlock()
}

func (s *Service) pauseBatchCapability(capability JobCapability, until time.Time) {
	s.batchStateMu.Lock()
	if until.After(s.batchPaused[capability]) {
		s.batchPaused[capability] = until.UTC()
	}
	s.batchStateMu.Unlock()
}

func (s *Service) batchCapabilityPaused(capability JobCapability, now time.Time) bool {
	s.batchStateMu.Lock()
	defer s.batchStateMu.Unlock()
	until := s.batchPaused[capability]
	if until.IsZero() || !until.After(now) {
		delete(s.batchPaused, capability)
		return false
	}
	return true
}
