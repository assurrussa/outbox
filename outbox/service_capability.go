package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const leaseFinalizationTimeout = 5 * time.Second

func (s *Service) findAndProcessJob(
	ctx context.Context,
	log logger.Logger,
	capabilities []JobCapability,
) error {
	if s.capabilityJobsRepo == nil {
		return s.findAndProcessLegacyJob(ctx, log)
	}

	return s.findAndProcessCapabilityJob(ctx, log, capabilities)
}

func (s *Service) findAndProcessCapabilityJob(
	ctx context.Context,
	log logger.Logger,
	capabilities []JobCapability,
) error {
	if len(capabilities) == 0 {
		return sharederrors.ErrNoJobs
	}

	now := time.Now().UTC()
	leaseToken := types.NewLeaseToken()
	job, err := func() (models.Job, error) {
		s.claimMu.RLock()
		defer s.claimMu.RUnlock()
		if s.IsDraining() {
			return models.Job{}, ErrServiceDraining
		}
		return s.capabilityJobsRepo.FindAndReserveJobForCapabilities(
			ctx,
			now,
			now.Add(s.reserveFor),
			leaseToken,
			capabilities,
		)
	}()
	if err != nil {
		return fmt.Errorf("find and reserve capability job: %w", err)
	}

	capability := JobCapability{
		Name:          job.Name,
		SchemaVersion: job.SchemaVersion,
	}
	if job.LeaseToken != leaseToken {
		return fmt.Errorf("%w: invalid lease token for job %s", ErrLeaseLost, job.ID)
	}

	s.mu.RLock()
	handler, ok := s.jobs[capability]
	s.mu.RUnlock()
	if !ok {
		log.ErrorContext(ctx, "repository claimed unsupported job capability",
			slog.String("job_name", job.Name),
			slog.Int64("schema_version", int64(job.SchemaVersion)),
			slog.String("job_id", job.ID.String()),
		)
		return fmt.Errorf(
			"%w: %s schema version %d",
			ErrUnsupportedClaim,
			job.Name,
			job.SchemaVersion,
		)
	}

	err = s.executeLeasedJob(ctx, handler, job)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		log.ErrorContext(ctx, "handle capability job error",
			logger.Error(err),
			slog.String("job_name", job.Name),
			slog.Int64("schema_version", int64(job.SchemaVersion)),
			slog.String("job_id", job.ID.String()),
			slog.Int("attempt_number", job.Attempts),
		)

		if errors.Is(err, ErrLeaseLost) {
			return err
		}

		if job.Attempts >= handler.MaxAttempts() {
			return s.dlqLeased(
				ctx,
				job,
				fmt.Sprintf("max attempts exceeded: %v", err),
			)
		}

		return nil
	}

	return s.ackLeased(ctx, job)
}

func (s *Service) executeLeasedJob(ctx context.Context, handler Job, job models.Job) error {
	handleCtx, cancelHandler := context.WithTimeout(ctx, handler.ExecutionTimeout())
	defer cancelHandler()

	heartbeatCtx, stopHeartbeat := context.WithCancel(handleCtx)
	heartbeatErr := make(chan error, 1)
	go func() {
		heartbeatErr <- s.heartbeatJob(heartbeatCtx, job, cancelHandler)
	}()

	handleErr := s.handleJob(handleCtx, handler, job)
	stopHeartbeat()
	if err := <-heartbeatErr; err != nil {
		return err
	}

	return handleErr
}

func (s *Service) heartbeatJob(
	ctx context.Context,
	job models.Job,
	cancelHandler context.CancelFunc,
) error {
	ticker := time.NewTicker(s.reserveFor / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			now := time.Now().UTC()
			affected, err := s.capabilityJobsRepo.ExtendJobLease(
				ctx,
				job.ID,
				job.LeaseToken,
				now,
				now.Add(s.reserveFor),
			)
			if err != nil {
				cancelHandler()
				return errors.Join(ErrLeaseLost, fmt.Errorf("extend job lease: %w", err))
			}
			if affected != 1 {
				cancelHandler()
				return ErrLeaseLost
			}
		}
	}
}

func (s *Service) ackLeased(ctx context.Context, job models.Job) error {
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	affected, err := s.capabilityJobsRepo.DeleteJobWithLease(
		finalizeCtx,
		job.ID,
		job.LeaseToken,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("delete capability job: %w", err)
	}
	if affected != 1 {
		return ErrLeaseLost
	}

	return nil
}

func (s *Service) dlqLeased(ctx context.Context, job models.Job, reason string) error {
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseFinalizationTimeout)
	defer cancel()

	return s.transactor.RunInTx(finalizeCtx, func(txCtx context.Context) error {
		if _, err := s.capabilityJobsFailedRepo.CreateFailedJobVersioned(
			txCtx,
			job.ID,
			job.Name,
			job.SchemaVersion,
			job.Payload,
			reason,
		); err != nil {
			return fmt.Errorf("create versioned failed job: %w", err)
		}

		affected, err := s.capabilityJobsRepo.DeleteJobWithLease(
			txCtx,
			job.ID,
			job.LeaseToken,
			time.Now().UTC(),
		)
		if err != nil {
			return fmt.Errorf("delete capability job: %w", err)
		}
		if affected != 1 {
			return ErrLeaseLost
		}

		return nil
	})
}
