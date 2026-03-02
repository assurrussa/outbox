package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const serviceName = "outbox"

var ErrServiceRunning = errors.New("outbox service is already running")

type Service struct {
	Options
	jobs    map[string]Job
	mu      sync.RWMutex
	running atomic.Bool
}

func New(options ...OptOptionsSetter) (*Service, error) {
	opts, err := NewOptions(options...)
	if err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	return &Service{
		Options: opts,
		jobs:    make(map[string]Job),
	}, nil
}

func (s *Service) RegisterJob(job Job) error {
	if job == nil {
		return errors.New("nil job")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running.Load() {
		return ErrServiceRunning
	}

	if _, ok := s.jobs[job.Name()]; ok {
		return fmt.Errorf("job %q already registered", job.Name())
	}

	s.jobs[job.Name()] = job

	return nil
}

func (s *Service) MustRegisterJob(job Job) {
	if err := s.RegisterJob(job); err != nil {
		panic(fmt.Errorf("register job: %w", err))
	}
}

func (s *Service) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrServiceRunning
	}
	defer s.running.Store(false)

	eg, ctx := errgroup.WithContext(ctx)

	for i := 0; i < s.workers; i++ {
		log := logger.WrapWithAttrs(s.logger, slog.Int("worker", i+1))
		eg.Go(func() error {
			defer func() {
				log.InfoContext(ctx, "finished worker")
			}()
			log.InfoContext(ctx, "start worker")

			for {
				// Process all available jobsrepo in one go.
				if err := s.processAvailableJobs(ctx, log); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					log.WarnContext(ctx, "process jobsrepo error", logger.Error(err))
					return err
				}

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(s.idleTime):
				}
			}
		})
	}

	return eg.Wait()
}

func (s *Service) processAvailableJobs(ctx context.Context, log logger.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := s.findAndProcessJob(ctx, log); err != nil {
			if errors.Is(err, sharederrors.ErrNoJobs) {
				log.DebugContext(ctx, "no jobsrepo found to process")
				return nil
			}
			return err
		}
	}
}

func (s *Service) findAndProcessJob(ctx context.Context, log logger.Logger) error {
	job, err := s.jobsRepo.FindAndReserveJob(ctx, time.Now().Local(), time.Now().Local().Add(s.reserveFor))
	if err != nil {
		return fmt.Errorf("find and reserve job: %w", err)
	}

	s.mu.RLock()
	j, ok := s.jobs[job.Name]
	s.mu.RUnlock()
	if !ok {
		log.WarnContext(ctx, "drop to dlq: job is not registered",
			slog.String("job_name", job.Name),
			slog.String("job_id", job.ID.String()),
			slog.Int("attempt_number", job.Attempts),
		)
		return s.dlq(ctx, job.ID, job.Name, job.Payload, "unknown job")
	}

	err = s.executeJob(ctx, j, job)
	if err != nil {
		log.ErrorContext(ctx, "handle job error",
			logger.Error(err),
			slog.String("job_name", job.Name),
			slog.String("job_id", job.ID.String()),
			slog.Int("attempt_number", job.Attempts),
		)

		if job.Attempts >= j.MaxAttempts() {
			log.WarnContext(ctx, "drop to dlq: job max attempts exceeded",
				slog.String("job_name", job.Name),
				slog.String("job_id", job.ID.String()),
				slog.Int("attempt_number", job.Attempts),
			)
			return s.dlq(
				ctx,
				job.ID,
				job.Name,
				job.Payload,
				fmt.Sprintf("max attempts exceeded: %v", err),
			)
		}
		return nil
	}

	if _, err := s.jobsRepo.DeleteJob(context.WithoutCancel(ctx), job.ID); err != nil {
		log.ErrorContext(ctx, "delete job error",
			logger.Error(err),
			slog.String("job_name", job.Name),
			slog.String("job_id", job.ID.String()),
			slog.Int("attempt_number", job.Attempts),
		)
	}

	return nil
}

func (s *Service) executeJob(ctx context.Context, j Job, job models.Job) (err error) {
	ctx, cancel := context.WithTimeout(ctx, j.ExecutionTimeout())
	defer cancel()

	ctx = withJobID(ctx, job.ID)

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in job %q: %v", job.Name, r)
		}
	}()

	return j.Handle(ctx, job.Payload)
}

func (s *Service) dlq(ctx context.Context, jobID types.JobID, name, payload, reason string) error {
	return s.transactor.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := s.jobsFailedRepo.CreateFailedJob(ctx, jobID, name, payload, reason); err != nil {
			return fmt.Errorf("create failed job: %w", err)
		}

		if _, err := s.jobsRepo.DeleteJob(ctx, jobID); err != nil {
			return fmt.Errorf("delete job: %w", err)
		}

		return nil
	})
}
