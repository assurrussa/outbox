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

var (
	ErrServiceRunning    = errors.New("outbox service is already running")
	ErrServiceNotRunning = errors.New("outbox service is not running")
	ErrServiceDraining   = errors.New("outbox service is draining")
)

type Service struct {
	Options
	jobs    map[JobCapability]Job
	mu      sync.RWMutex
	running atomic.Bool
	ready   atomic.Bool
	claimMu sync.RWMutex
	drain   chan struct{}
	drainDo sync.Once
}

func New(options ...OptOptionsSetter) (*Service, error) {
	opts, err := NewOptions(options...)
	if err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	service := &Service{
		Options: opts,
		jobs:    make(map[JobCapability]Job),
		drain:   make(chan struct{}),
	}

	if opts.fanoutJobsRepo != nil {
		if err := service.RegisterJob(fanoutDispatcher{
			repo:       opts.fanoutJobsRepo,
			transactor: opts.transactor,
		}); err != nil {
			return nil, fmt.Errorf("register fan-out dispatcher: %w", err)
		}
	}

	return service, nil
}

func (s *Service) RegisterJob(job Job) error {
	return s.RegisterJobs(job)
}

// RegisterJobs validates and installs one capability batch atomically.
func (s *Service) RegisterJobs(jobs ...Job) error {
	capabilities := make(map[JobCapability]Job, len(jobs))
	for index, job := range jobs {
		if job == nil {
			return fmt.Errorf("job %d is nil", index)
		}
		capability, err := capabilityForJob(job)
		if err != nil {
			return fmt.Errorf("job %d capability: %w", index, err)
		}
		if capability.SchemaVersion != DefaultSchemaVersion && s.capabilityJobsRepo == nil {
			return ErrCapabilityRepositoryNotConfigured
		}
		if _, duplicate := capabilities[capability]; duplicate {
			return fmt.Errorf(
				"job %q schema version %d is duplicated in registration batch",
				capability.Name,
				capability.SchemaVersion,
			)
		}
		capabilities[capability] = job
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running.Load() {
		return ErrServiceRunning
	}

	for capability := range capabilities {
		if _, ok := s.jobs[capability]; ok {
			return fmt.Errorf(
				"job %q schema version %d already registered",
				capability.Name,
				capability.SchemaVersion,
			)
		}
	}
	for capability, job := range capabilities {
		s.jobs[capability] = job
	}

	return nil
}

func (s *Service) MustRegisterJob(job Job) {
	if err := s.RegisterJob(job); err != nil {
		panic(fmt.Errorf("register job: %w", err))
	}
}

// MustRegisterJobs installs one batch or panics without partially registering it.
func (s *Service) MustRegisterJobs(jobs ...Job) {
	if err := s.RegisterJobs(jobs...); err != nil {
		panic(fmt.Errorf("register jobs: %w", err))
	}
}

func (s *Service) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrServiceRunning
	}
	defer s.running.Store(false)
	defer s.ready.Store(false)

	eg, ctx := errgroup.WithContext(ctx)
	capabilities := s.registeredCapabilities()

	for i := 0; i < s.workers; i++ {
		log := logger.WrapWithAttrs(s.logger, slog.Int("worker", i+1))
		eg.Go(func() error {
			defer func() {
				log.InfoContext(ctx, "finished worker")
			}()
			log.InfoContext(ctx, "start worker")

			for {
				if s.IsDraining() {
					return nil
				}
				// Process all available jobsrepo in one go.
				if err := s.processAvailableJobs(ctx, log, capabilities); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					log.WarnContext(ctx, "process jobsrepo error", logger.Error(err))
					return err
				}

				select {
				case <-ctx.Done():
					return nil
				case <-s.drain:
					return nil
				case <-time.After(s.idleTime):
				}
			}
		})
	}
	if !s.IsDraining() {
		s.ready.Store(true)
	}

	return eg.Wait()
}

// Readiness reports only the worker lifecycle state and never reserves a job.
// Hosts pair it with their database probe before opening role readiness.
func (s *Service) Readiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !s.running.Load() {
		return ErrServiceNotRunning
	}
	if s.IsDraining() {
		return ErrServiceDraining
	}
	if !s.ready.Load() {
		return ErrServiceNotRunning
	}
	return nil
}

// BeginDrain atomically closes the claim boundary. After it returns no worker
// can start another repository claim, while handlers for already reserved jobs
// keep their original context and lease heartbeat until they finish or the
// caller cancels the Run context at its bounded drain deadline.
func (s *Service) BeginDrain() {
	if s == nil {
		return
	}
	s.ready.Store(false)
	s.claimMu.Lock()
	s.drainDo.Do(func() { close(s.drain) })
	s.claimMu.Unlock()
}

func (s *Service) IsDraining() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.drain:
		return true
	default:
		return false
	}
}

func (s *Service) processAvailableJobs(
	ctx context.Context,
	log logger.Logger,
	capabilities []JobCapability,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.drain:
			return nil
		default:
		}

		if err := s.findAndProcessJob(ctx, log, capabilities); err != nil {
			if errors.Is(err, sharederrors.ErrNoJobs) || errors.Is(err, ErrServiceDraining) {
				log.DebugContext(ctx, "no jobsrepo found to process")
				return nil
			}
			return err
		}
	}
}

func (s *Service) findAndProcessLegacyJob(ctx context.Context, log logger.Logger) error {
	job, err := func() (models.Job, error) {
		s.claimMu.RLock()
		defer s.claimMu.RUnlock()
		if s.IsDraining() {
			return models.Job{}, ErrServiceDraining
		}
		return s.jobsRepo.FindAndReserveJob(ctx, time.Now().Local(), time.Now().Local().Add(s.reserveFor))
	}()
	if err != nil {
		return fmt.Errorf("find and reserve job: %w", err)
	}

	capability := JobCapability{
		Name:          job.Name,
		SchemaVersion: normalizeSchemaVersion(job.SchemaVersion),
	}

	s.mu.RLock()
	j, ok := s.jobs[capability]
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

		if IsPermanent(err) {
			log.WarnContext(ctx, "drop to dlq: permanent job failure",
				slog.String("job_name", job.Name),
				slog.String("job_id", job.ID.String()),
				slog.Int("attempt_number", job.Attempts),
			)
			return s.dlq(
				ctx,
				job.ID,
				job.Name,
				job.Payload,
				fmt.Sprintf("permanent failure: %v", err),
			)
		}

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

	return s.handleJob(ctx, j, job)
}

func (s *Service) handleJob(ctx context.Context, j Job, job models.Job) (err error) {
	ctx = withJobMetadata(ctx, job)

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
