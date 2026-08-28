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
	outstanding map[types.JobID]struct{}

	mu          sync.Mutex
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
		outstanding: make(map[types.JobID]struct{}, len(jobs)),
		stop:        stop,
		done:        make(chan struct{}),
		cancelBatch: cancelBatch,
	}
	for _, job := range jobs {
		manager.orderedIDs = append(manager.orderedIDs, job.ID)
		manager.outstanding[job.ID] = struct{}{}
	}

	go manager.run(heartbeatCtx)

	return manager
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
				m.setError(err)
				m.cancelBatch(err)
				return
			}
		}
	}
}

func (m *batchLeaseManager) extend(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobIDs := m.outstandingIDsLocked()
	if len(jobIDs) == 0 {
		return nil
	}

	now := time.Now().UTC()
	affected, err := m.repo.ExtendJobLeases(
		ctx,
		jobIDs,
		m.leaseToken,
		now,
		now.Add(m.reserveFor),
	)
	if err != nil {
		return errors.Join(ErrLeaseLost, fmt.Errorf("extend reservation batch leases: %w", err))
	}
	if affected != int64(len(jobIDs)) {
		return fmt.Errorf(
			"%w: extended %d of %d reservation batch leases",
			ErrLeaseLost,
			affected,
			len(jobIDs),
		)
	}

	return nil
}

func (m *batchLeaseManager) finalize(jobID types.JobID, finalize func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.outstanding[jobID]; !ok {
		return ErrLeaseLost
	}
	if err := finalize(); err != nil {
		return err
	}
	delete(m.outstanding, jobID)

	return nil
}

func (m *batchLeaseManager) forget(jobID types.JobID) {
	m.mu.Lock()
	delete(m.outstanding, jobID)
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
		select {
		case <-ctx.Done():
			processErr = ctx.Err()
		case <-s.drain:
			processErr = ErrServiceDraining
		default:
		}
		if processErr != nil {
			break
		}

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

func (s *Service) claimBatch(
	ctx context.Context,
	capabilities []JobCapability,
) ([]models.Job, LeaseToken, error) {
	if len(capabilities) == 0 {
		return nil, LeaseToken{}, ErrNoJobs
	}

	now := time.Now().UTC()
	leaseToken := types.NewLeaseToken()

	s.claimMu.RLock()
	defer s.claimMu.RUnlock()
	if s.IsDraining() {
		return nil, LeaseToken{}, ErrServiceDraining
	}

	jobs, err := s.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx,
		now,
		now.Add(s.reserveFor),
		leaseToken,
		capabilities,
		s.reservationBatchSize,
	)
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
		if job.LeaseToken != leaseToken {
			return nil, LeaseToken{}, fmt.Errorf(
				"%w: invalid lease token for job %s",
				ErrLeaseLost,
				job.ID,
			)
		}
	}

	return jobs, leaseToken, nil
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
			return true, manager.finalize(job.ID, func() error {
				return s.dlqBatch(ctx, manager.repo, job, fmt.Sprintf("permanent failure: %v", handleErr))
			})
		case job.Attempts >= handler.MaxAttempts():
			return true, manager.finalize(job.ID, func() error {
				return s.dlqBatch(ctx, manager.repo, job, fmt.Sprintf("max attempts exceeded: %v", handleErr))
			})
		default:
			if availableAt, ok := RetryTime(handleErr); ok {
				return true, manager.finalize(job.ID, func() error {
					return s.rescheduleLeased(ctx, job, availableAt)
				})
			}
		}

		manager.forget(job.ID)
		return true, nil
	}

	return true, manager.finalize(job.ID, func() error {
		return s.ackBatch(ctx, manager.repo, job)
	})
}

func (s *Service) ackBatch(
	ctx context.Context,
	repo JobsRepository,
	job models.Job,
) error {
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	affected, err := repo.DeleteJobWithLease(
		finalizeCtx,
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
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	return s.transactor.RunInTx(finalizeCtx, func(txCtx context.Context) error {
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
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	now := time.Now().UTC()
	if availableAt.Before(now) {
		availableAt = now
	}
	affected, err := s.jobsRepo.RescheduleJobWithLease(
		finalizeCtx,
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
