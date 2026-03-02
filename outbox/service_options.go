package outbox

import (
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
)

var ErrOption = errors.New("outbox invalid option")

type OptOptionsSetter func(o *Options)

type Options struct {
	workers        int
	idleTime       time.Duration
	reserveFor     time.Duration
	jobsRepo       JobsRepository
	jobsStatRepo   JobsStatRepository
	jobsFailedRepo JobsFailedRepository
	transactor     Transactor
	logger         logger.Logger
}

func NewOptions(options ...OptOptionsSetter) (Options, error) {
	o := Options{
		workers:        1,
		idleTime:       time.Second,
		reserveFor:     5 * time.Minute,
		logger:         logger.WrapNamed(logger.Default(), serviceName),
		jobsRepo:       nil,
		jobsFailedRepo: nil,
		transactor:     nil,
	}

	for _, opt := range options {
		opt(&o)
	}

	if err := o.Validate(); err != nil {
		return o, errors.Join(ErrOption, err)
	}

	return o, nil
}

func (o *Options) Validate() error {
	if o == nil {
		return errors.New("nil options")
	}
	if o.jobsRepo == nil {
		return errors.New("nil jobsRepo")
	}
	if o.jobsFailedRepo == nil {
		return errors.New("nil jobsFailedRepo")
	}
	if o.transactor == nil {
		return errors.New("nil transactor")
	}
	if o.logger == nil {
		return errors.New("nil logger")
	}
	if o.workers < 1 || o.workers > 32 {
		return fmt.Errorf("invalid number of workers: %d", o.workers)
	}
	if o.idleTime < 100*time.Millisecond || o.idleTime > 10*time.Second {
		return fmt.Errorf("invalid idle time: %s", o.idleTime)
	}
	if o.reserveFor < 1*time.Second || o.reserveFor > 10*time.Minute {
		return fmt.Errorf("invalid reserve for: %s", o.reserveFor)
	}
	return nil
}

func WithWorkers(workers int) OptOptionsSetter {
	return func(o *Options) {
		o.workers = workers
	}
}

func WithIdleTime(idleTime time.Duration) OptOptionsSetter {
	return func(o *Options) {
		o.idleTime = idleTime
	}
}

func WithReserveFor(reserveFor time.Duration) OptOptionsSetter {
	return func(o *Options) {
		o.reserveFor = reserveFor
	}
}

func WithLogger(logger logger.Logger) OptOptionsSetter {
	return func(o *Options) {
		o.logger = logger
	}
}

func WithTransactor(transactor Transactor) OptOptionsSetter {
	return func(o *Options) {
		o.transactor = transactor
	}
}

func WithJobsRepo(jobsRepo JobsRepository) OptOptionsSetter {
	return func(o *Options) {
		o.jobsRepo = jobsRepo
	}
}

// WithJobsStatRepo configures optional queue stats provider.
// It is required only for Service.GetQueueStats.
func WithJobsStatRepo(jobsStatRepo JobsStatRepository) OptOptionsSetter {
	return func(o *Options) {
		o.jobsStatRepo = jobsStatRepo
	}
}

func WithJobsFailedRepo(jobsFailedRepo JobsFailedRepository) OptOptionsSetter {
	return func(o *Options) {
		o.jobsFailedRepo = jobsFailedRepo
	}
}
