package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const leaseFinalizationTimeout = 5 * time.Second

type batchLeaseManager struct {
	repo        JobsRepository
	leaseToken  LeaseToken
	reserveFor  time.Duration
	orderedIDs  []types.JobID
	outstanding map[types.JobID]time.Time
	// earliestUntil is a conservative lower bound. Removing a row cannot
	// invalidate it; refresh only when a caller needs a stronger bound.
	earliestUntil time.Time

	mu          leaseMutex
	errMu       sync.Mutex
	err         error
	stop        context.CancelFunc
	done        chan struct{}
	cancelBatch context.CancelCauseFunc
}

func newBatchLeaseManager(
	ctx context.Context,
	repo JobsRepository,
	jobs []models.Job,
	leaseToken LeaseToken,
	reserveFor time.Duration,
	cancelBatch context.CancelCauseFunc,
) *batchLeaseManager {
	heartbeatCtx, stop := context.WithCancel(ctx)
	manager := &batchLeaseManager{
		repo:        repo,
		leaseToken:  leaseToken,
		reserveFor:  reserveFor,
		orderedIDs:  make([]types.JobID, 0, len(jobs)),
		outstanding: make(map[types.JobID]time.Time, len(jobs)),
		stop:        stop,
		done:        make(chan struct{}),
		cancelBatch: cancelBatch,
	}
	for _, job := range jobs {
		manager.orderedIDs = append(manager.orderedIDs, job.ID)
		manager.outstanding[job.ID] = job.ReservedAt.Time
		if manager.earliestUntil.IsZero() || job.ReservedAt.Time.Before(manager.earliestUntil) {
			manager.earliestUntil = job.ReservedAt.Time
		}
	}

	go manager.run(heartbeatCtx)

	return manager
}

func (m *batchLeaseManager) recomputeEarliestLocked() {
	var earliest time.Time
	for _, until := range m.outstanding {
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	m.earliestUntil = earliest
}

func (m *batchLeaseManager) run(ctx context.Context) {
	defer close(m.done)

	ticker := time.NewTicker(m.reserveFor / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.extend(ctx); err != nil {
				return
			}
		}
	}
}

func (m *batchLeaseManager) extend(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := time.Now().UTC().Add(m.reserveFor)
	return m.ensureLeasesLocked(ctx, target, target)
}

// ensureLeasesLocked never shortens a lease. Its deadline ledger is updated
// only after a fully successful fenced extension, including a freshness check
// after the repository call. Millisecond-aligned deadlines round-trip through
// every built-in backend, including SQLite.
func (m *batchLeaseManager) ensureLeasesLocked(ctx context.Context, requiredUntil, targetUntil time.Time) error {
	now := time.Now().UTC()
	if err := m.checkLeasesLocked(now); err != nil {
		return err
	}
	if !m.earliestUntil.Before(requiredUntil) {
		return nil
	}
	var jobIDs []types.JobID
	var earliest time.Time
	for _, jobID := range m.orderedIDs {
		until, ok := m.outstanding[jobID]
		if !ok {
			continue
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
		if until.Before(requiredUntil) {
			jobIDs = append(jobIDs, jobID)
		}
	}
	m.earliestUntil = earliest
	if len(jobIDs) == 0 {
		return nil
	}
	if targetUntil.Before(requiredUntil) {
		targetUntil = requiredUntil
	}
	until := targetUntil.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	extendCtx, cancel := context.WithDeadline(ctx, earliest)
	defer cancel()
	affected, err := m.repo.ExtendJobLeases(extendCtx, jobIDs, m.leaseToken, now, until)
	if err == nil {
		err = context.Cause(extendCtx)
	}
	if err != nil {
		return m.failLeaseLocked(errors.Join(ErrLeaseLost, fmt.Errorf("extend reservation batch leases: %w", err)))
	}
	if affected != int64(len(jobIDs)) {
		return m.failLeaseLocked(fmt.Errorf("%w: extended %d of %d reservation batch leases", ErrLeaseLost, affected, len(jobIDs)))
	}
	for _, jobID := range jobIDs {
		m.outstanding[jobID] = until
	}
	m.recomputeEarliestLocked()
	return m.checkLeasesLocked(time.Now().UTC())
}

func (m *batchLeaseManager) checkLeasesLocked(now time.Time) error {
	if now.Before(m.earliestUntil) {
		return nil
	}
	var earliest time.Time
	for jobID, until := range m.outstanding {
		if !until.After(now) {
			return m.failLeaseLocked(fmt.Errorf("%w: expired lease for job %s", ErrLeaseLost, jobID))
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	m.earliestUntil = earliest
	return nil
}

func (m *batchLeaseManager) failLeaseLocked(err error) error {
	m.setError(err)
	m.cancelBatch(err)

	return err
}

func (m *batchLeaseManager) admit(ctx context.Context, drain <-chan struct{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := batchStartError(ctx, drain); err != nil {
		return err
	}
	if len(m.outstanding) == 0 {
		return ErrLeaseLost
	}
	now := time.Now().UTC()
	if err := m.checkLeasesLocked(now); err != nil {
		return err
	}
	if m.earliestUntil.Before(now.Add(m.reserveFor / 3)) {
		target := now.Add(m.reserveFor)
		if err := m.ensureLeasesLocked(ctx, target, target); err != nil {
			return err
		}
	}
	return batchStartError(ctx, drain)
}

func (m *batchLeaseManager) finalize(ctx context.Context, jobID types.JobID, finalize func(context.Context) error) error {
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()
	deadline, _ := finalizeCtx.Deadline()
	if err := m.mu.LockContext(finalizeCtx); err != nil {
		m.cancelBatch(err)
		return err
	}
	defer m.mu.Unlock()

	if _, ok := m.outstanding[jobID]; !ok {
		return ErrLeaseLost
	}
	requiredUntil := deadline.Add(batchFinalizationMargin)
	targetUntil := requiredUntil.Add(m.reserveFor)
	if err := m.ensureLeasesLocked(finalizeCtx, requiredUntil, targetUntil); err != nil {
		return err
	}
	if err := finalizeCtx.Err(); err != nil {
		return err
	}
	if err := finalize(finalizeCtx); err != nil {
		return err
	}
	delete(m.outstanding, jobID)
	if len(m.outstanding) == 0 {
		m.earliestUntil = time.Time{}
	}
	return nil
}

func (m *batchLeaseManager) add(jobs []models.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range jobs {
		if err := validateClaimedJobLease(job, m.leaseToken, time.Now().UTC()); err != nil {
			return err
		}
		if _, duplicate := m.outstanding[job.ID]; duplicate {
			return fmt.Errorf("%w: duplicate claimed job %s", ErrLeaseLost, job.ID)
		}
		m.orderedIDs = append(m.orderedIDs, job.ID)
		m.outstanding[job.ID] = job.ReservedAt.Time
		if m.earliestUntil.IsZero() || job.ReservedAt.Time.Before(m.earliestUntil) {
			m.earliestUntil = job.ReservedAt.Time
		}
	}
	return nil
}

func (m *batchLeaseManager) finalizeAll(
	ctx context.Context,
	leaseUntil time.Time,
	finalize func() error,
) error {
	if err := m.mu.LockContext(ctx); err != nil {
		m.cancelBatch(err)
		return err
	}
	defer m.mu.Unlock()
	if len(m.outstanding) == 0 {
		return ErrLeaseLost
	}
	if err := m.ensureLeasesLocked(ctx, leaseUntil, leaseUntil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := finalize(); err != nil {
		return err
	}
	clear(m.outstanding)
	m.earliestUntil = time.Time{}
	return nil
}

func (m *batchLeaseManager) forget(jobID types.JobID) {
	m.mu.Lock()
	delete(m.outstanding, jobID)
	if len(m.outstanding) == 0 {
		m.earliestUntil = time.Time{}
	}
	m.mu.Unlock()
}

func (m *batchLeaseManager) forgetAll() {
	m.mu.Lock()
	clear(m.outstanding)
	m.earliestUntil = time.Time{}
	m.mu.Unlock()
}

func (m *batchLeaseManager) releaseUnstarted(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobIDs := m.outstandingIDsLocked()
	if len(jobIDs) == 0 {
		return nil
	}

	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	affected, err := m.repo.ReleaseUnstartedJobsWithLease(
		finalizeCtx,
		jobIDs,
		m.leaseToken,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("release unstarted reservation batch jobs: %w", err)
	}
	if affected != int64(len(jobIDs)) {
		return fmt.Errorf(
			"%w: released %d of %d unstarted reservation batch jobs",
			ErrLeaseLost,
			affected,
			len(jobIDs),
		)
	}
	clear(m.outstanding)
	m.earliestUntil = time.Time{}

	return nil
}

func (m *batchLeaseManager) outstandingIDsLocked() []types.JobID {
	jobIDs := make([]types.JobID, 0, len(m.outstanding))
	for _, jobID := range m.orderedIDs {
		if _, ok := m.outstanding[jobID]; ok {
			jobIDs = append(jobIDs, jobID)
		}
	}

	return jobIDs
}

func (m *batchLeaseManager) stopAndWait() error {
	m.stop()
	<-m.done

	m.errMu.Lock()
	defer m.errMu.Unlock()

	return m.err
}

func (m *batchLeaseManager) setError(err error) {
	m.errMu.Lock()
	m.err = err
	m.errMu.Unlock()
}

func (s *Service) findAndProcessBatch(
	ctx context.Context,
	log logger.Logger,
	capabilities []JobCapability,
) error {
	jobs, leaseToken, err := s.claimBatch(ctx, capabilities)
	if err != nil {
		return err
	}

	batchCtx, cancelBatch := context.WithCancelCause(ctx)
	defer cancelBatch(nil)
	manager := newBatchLeaseManager(
		batchCtx,
		s.jobsRepo,
		jobs,
		leaseToken,
		s.reserveFor,
		cancelBatch,
	)

	var processErr error
	for index := range jobs {
		job := jobs[index]
		started, err := s.processBatchJob(batchCtx, log, manager, job)
		if err == nil {
			continue
		}
		processErr = err
		if started {
			manager.forget(job.ID)
		}
		break
	}

	heartbeatErr := manager.stopAndWait()
	if processErr == nil && heartbeatErr != nil {
		processErr = heartbeatErr
	}
	releaseErr := manager.releaseUnstarted(ctx)

	if errors.Is(processErr, ErrServiceDraining) && releaseErr == nil && heartbeatErr == nil {
		return ErrServiceDraining
	}
	if processErr == nil && releaseErr == nil {
		return nil
	}

	return errors.Join(processErr, heartbeatErr, releaseErr)
}

func batchStartError(ctx context.Context, drain <-chan struct{}) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}

	select {
	case <-drain:
		return ErrServiceDraining
	default:
		return nil
	}
}

func (s *Service) claimBatch(
	ctx context.Context,
	capabilities []JobCapability,
) ([]models.Job, LeaseToken, error) {
	if len(capabilities) == 0 {
		return nil, LeaseToken{}, ErrNoJobs
	}

	now := time.Now().UTC()
	eligible := make([]JobCapability, 0, len(capabilities))
	for _, capability := range capabilities {
		if !s.batchCapabilityPaused(capability, now) {
			eligible = append(eligible, capability)
		}
	}
	if len(eligible) == 0 {
		return nil, LeaseToken{}, ErrNoJobs
	}
	leaseToken := types.NewLeaseToken()

	jobs, err := s.claimJobsWithLease(ctx, leaseToken, func(claimCtx context.Context, now, until time.Time) ([]models.Job, error) {
		return s.jobsRepo.FindAndReserveJobsForCapabilities(claimCtx, now, until, leaseToken, eligible, s.reservationBatchSize)
	})
	if err != nil {
		return nil, LeaseToken{}, fmt.Errorf("find and reserve job batch: %w", err)
	}
	if len(jobs) == 0 {
		return nil, LeaseToken{}, ErrEmptyReservationBatch
	}
	if len(jobs) > s.reservationBatchSize {
		return nil, LeaseToken{}, fmt.Errorf(
			"reservation batch repository returned %d jobs for limit %d",
			len(jobs),
			s.reservationBatchSize,
		)
	}
	for _, job := range jobs {
		if err := validateClaimedJobLease(job, leaseToken, time.Now().UTC()); err != nil {
			return nil, LeaseToken{}, err
		}
	}

	return jobs, leaseToken, nil
}

func validateClaimedJobLease(job models.Job, token LeaseToken, now time.Time) error {
	if job.LeaseToken != token {
		return fmt.Errorf("%w: invalid lease token for job %s", ErrLeaseLost, job.ID)
	}
	if !job.ReservedAt.Valid || !job.ReservedAt.Time.After(now) {
		return fmt.Errorf("%w: invalid or expired claimed lease for job %s", ErrLeaseLost, job.ID)
	}
	return nil
}

func (s *Service) processBatchJob(
	ctx context.Context,
	log logger.Logger,
	manager *batchLeaseManager,
	job models.Job,
) (bool, error) {
	capability := JobCapability{
		Name:          job.Name,
		SchemaVersion: normalizeSchemaVersion(job.SchemaVersion),
	}
	s.mu.RLock()
	handler, ok := s.jobs[capability]
	s.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf(
			"%w: %s schema version %d",
			ErrUnsupportedClaim,
			job.Name,
			job.SchemaVersion,
		)
	}
	if err := manager.admit(ctx, s.drain); err != nil {
		return false, err
	}

	handleErr := s.executeJob(ctx, handler, job)
	if cause := context.Cause(ctx); cause != nil {
		return true, cause
	}
	if handleErr != nil {
		log.ErrorContext(ctx, "handle reservation batch job error",
			logger.Error(handleErr),
			slog.String("job_name", job.Name),
			slog.Int64("schema_version", int64(job.SchemaVersion)),
			slog.String("job_id", job.ID.String()),
			slog.Int("attempt_number", job.Attempts),
		)

		switch {
		case IsPermanent(handleErr):
			return true, manager.finalize(ctx, job.ID, func(finalizeCtx context.Context) error {
				return s.dlqBatch(finalizeCtx, manager.repo, job, fmt.Sprintf("permanent failure: %v", handleErr))
			})
		case hasDeferTime(handleErr):
			availableAt, _ := DeferTime(handleErr)
			return true, manager.finalize(ctx, job.ID, func(finalizeCtx context.Context) error {
				return s.deferLeased(finalizeCtx, job, availableAt)
			})
		case job.Attempts >= handler.MaxAttempts():
			return true, manager.finalize(ctx, job.ID, func(finalizeCtx context.Context) error {
				return s.dlqBatch(finalizeCtx, manager.repo, job, fmt.Sprintf("max attempts exceeded: %v", handleErr))
			})
		default:
			if availableAt, ok := RetryTime(handleErr); ok {
				return true, manager.finalize(ctx, job.ID, func(finalizeCtx context.Context) error {
					return s.rescheduleLeased(finalizeCtx, job, availableAt)
				})
			}
		}

		manager.forget(job.ID)
		return true, nil
	}

	return true, manager.finalize(ctx, job.ID, func(finalizeCtx context.Context) error {
		return s.ackBatch(finalizeCtx, manager.repo, job)
	})
}

func (s *Service) deferLeased(ctx context.Context, job models.Job, availableAt time.Time) error {
	repo, ok := s.jobsRepo.(DeferJobsRepository)
	if !ok {
		return ErrBatchRepositoryNotConfigured
	}
	now := time.Now().UTC()
	if availableAt.Before(now) {
		availableAt = now
	}
	affected, err := repo.DeferJobWithLease(
		ctx,
		job.ID,
		job.LeaseToken,
		now,
		availableAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("defer leased job: %w", err)
	}
	if affected != 1 {
		return ErrLeaseLost
	}

	s.pauseBatchCapability(JobCapability{Name: job.Name, SchemaVersion: job.SchemaVersion}, availableAt)
	return nil
}

func (s *Service) ackBatch(
	ctx context.Context,
	repo JobsRepository,
	job models.Job,
) error {
	affected, err := repo.DeleteJobWithLease(
		ctx,
		job.ID,
		job.LeaseToken,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("delete reservation batch job: %w", err)
	}
	if affected != 1 {
		return ErrLeaseLost
	}

	return nil
}

func (s *Service) dlqBatch(
	ctx context.Context,
	repo JobsRepository,
	job models.Job,
	reason string,
) error {
	return s.transactor.RunInTx(ctx, func(txCtx context.Context) error {
		if _, err := s.jobsFailedRepo.CreateFailedJobVersioned(
			txCtx,
			job.ID,
			job.Name,
			job.SchemaVersion,
			job.Payload,
			reason,
		); err != nil {
			return fmt.Errorf("create versioned failed job: %w", err)
		}

		affected, err := repo.DeleteJobWithLease(
			txCtx,
			job.ID,
			job.LeaseToken,
			time.Now().UTC(),
		)
		if err != nil {
			return fmt.Errorf("delete reservation batch job: %w", err)
		}
		if affected != 1 {
			return ErrLeaseLost
		}

		return nil
	})
}

func (s *Service) rescheduleLeased(ctx context.Context, job models.Job, availableAt time.Time) error {
	now := time.Now().UTC()
	if availableAt.Before(now) {
		availableAt = now
	}
	affected, err := s.jobsRepo.RescheduleJobWithLease(
		ctx,
		job.ID,
		job.LeaseToken,
		now,
		availableAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("reschedule job: %w", err)
	}
	if affected != 1 {
		return ErrLeaseLost
	}

	return nil
}
