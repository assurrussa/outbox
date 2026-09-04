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
)

const serviceName = "outbox"

var (
	ErrServiceRunning    = errors.New("outbox service is already running")
	ErrServiceNotRunning = errors.New("outbox service is not running")
	ErrServiceDraining   = errors.New("outbox service is draining")
)

type Service struct {
	Options
	jobs      map[JobCapability]Job
	batchJobs map[JobCapability]batchJobRegistration
	mu        sync.RWMutex
	running   atomic.Bool
	ready     atomic.Bool
	claimMu   sync.RWMutex
	drain     chan struct{}
	drainDo   sync.Once

	batchStateMu sync.Mutex
	batchPaused  map[JobCapability]time.Time
	batchStreak  map[JobCapability]int
}

type batchJobRegistration struct {
	job    BatchJob
	config normalizedBatchConfig
}

type workerSchedule struct {
	batchCursor int
	preferBatch bool
}

func New(options ...OptOptionsSetter) (*Service, error) {
	opts, err := NewOptions(options...)
	if err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	service := &Service{
		Options:     opts,
		jobs:        make(map[JobCapability]Job),
		batchJobs:   make(map[JobCapability]batchJobRegistration),
		drain:       make(chan struct{}),
		batchPaused: make(map[JobCapability]time.Time),
		batchStreak: make(map[JobCapability]int),
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
		if _, ok := s.batchJobs[capability]; ok {
			return fmt.Errorf(
				"job %q schema version %d already registered as a batch job",
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

// RegisterBatchJob validates and registers one real batch handler. It fails
// closed unless the configured repository implements the complete batch
// execution capability.
func (s *Service) RegisterBatchJob(job BatchJob, config BatchConfig) error {
	if job == nil {
		return errors.New("batch job is nil")
	}
	batchRepo, ok := s.jobsRepo.(BatchJobsRepository)
	if !ok {
		return ErrBatchRepositoryNotConfigured
	}
	normalized, err := config.normalize()
	if err != nil {
		return fmt.Errorf("batch config: %w", err)
	}
	if repositoryMax := batchRepo.MaxExecutionBatchSize(); repositoryMax < 1 || normalized.maxMessages > repositoryMax {
		return fmt.Errorf(
			"%w: requested %d, repository maximum %d",
			ErrReservationBatchSizeUnsupported,
			normalized.maxMessages,
			repositoryMax,
		)
	}
	capability, err := capabilityForBatchJob(job)
	if err != nil {
		return fmt.Errorf("batch job capability: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running.Load() {
		return ErrServiceRunning
	}
	if _, exists := s.jobs[capability]; exists {
		return fmt.Errorf(
			"job %q schema version %d already registered as a single job",
			capability.Name,
			capability.SchemaVersion,
		)
	}
	if _, exists := s.batchJobs[capability]; exists {
		return fmt.Errorf(
			"batch job %q schema version %d already registered",
			capability.Name,
			capability.SchemaVersion,
		)
	}
	s.batchJobs[capability] = batchJobRegistration{job: job, config: normalized}

	return nil
}

// MustRegisterBatchJob registers a real batch handler or panics.
func (s *Service) MustRegisterBatchJob(job BatchJob, config BatchConfig) {
	if err := s.RegisterBatchJob(job, config); err != nil {
		panic(fmt.Errorf("register batch job: %w", err))
	}
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
	singleCapabilities := s.registeredSingleCapabilities()
	batchCapabilities := s.registeredBatchCapabilities()

	for i := 0; i < s.workers; i++ {
		log := logger.WrapWithAttrs(s.logger, slog.Int("worker", i+1))
		schedule := workerSchedule{
			batchCursor: i,
			preferBatch: i%2 == 0,
		}
		eg.Go(func() error { return s.runWorker(ctx, log, singleCapabilities, batchCapabilities, schedule) })
	}
	if !s.IsDraining() {
		s.ready.Store(true)
	}

	return eg.Wait()
}

func (s *Service) runWorker(
	ctx context.Context,
	log logger.Logger,
	singleCapabilities []JobCapability,
	batchCapabilities []JobCapability,
	schedule workerSchedule,
) error {
	defer log.InfoContext(ctx, "finished worker")
	log.InfoContext(ctx, "start worker")

	for !s.IsDraining() {
		didWork, err := s.processWorkerWork(ctx, log, singleCapabilities, batchCapabilities, &schedule)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.WarnContext(ctx, "process jobsrepo error", logger.Error(err))
			return err
		}
		if didWork {
			continue
		}
		if s.waitForWorker(ctx) {
			return nil
		}
	}
	return nil
}

func (s *Service) processWorkerWork(
	ctx context.Context,
	log logger.Logger,
	singleCapabilities []JobCapability,
	batchCapabilities []JobCapability,
	schedule *workerSchedule,
) (bool, error) {
	if schedule.preferBatch {
		didWork, err := s.processOneAvailableBatch(ctx, log, batchCapabilities, &schedule.batchCursor)
		if errors.Is(err, ErrServiceDraining) {
			return false, nil
		}
		if err != nil || didWork {
			if didWork {
				schedule.preferBatch = false
			}
			return didWork, err
		}
	}

	didWork, err := s.processOneAvailableSingle(ctx, log, singleCapabilities)
	if err != nil || didWork {
		if didWork {
			schedule.preferBatch = true
		}
		return didWork, err
	}
	if schedule.preferBatch {
		return false, nil
	}

	didWork, err = s.processOneAvailableBatch(ctx, log, batchCapabilities, &schedule.batchCursor)
	if errors.Is(err, ErrServiceDraining) {
		return false, nil
	}
	if didWork {
		schedule.preferBatch = false
	}
	return didWork, err
}

func (s *Service) processOneAvailableSingle(
	ctx context.Context,
	log logger.Logger,
	singleCapabilities []JobCapability,
) (bool, error) {
	if len(singleCapabilities) == 0 {
		return false, nil
	}
	err := s.findAndProcessBatch(ctx, log, singleCapabilities)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNoJobs), errors.Is(err, ErrServiceDraining):
		return false, nil
	default:
		return false, err
	}
}

func (s *Service) waitForWorker(ctx context.Context) bool {
	timer := time.NewTimer(s.idleTime)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-s.drain:
		return true
	case <-timer.C:
		return false
	}
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

func (s *Service) executeJob(ctx context.Context, j Job, job models.Job) (err error) {
	handlerCtx, cancel := context.WithTimeout(ctx, j.ExecutionTimeout())
	defer cancel()

	err = s.handleJob(handlerCtx, j, job)
	if cause := context.Cause(handlerCtx); cause != nil {
		return cause
	}

	return err
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
