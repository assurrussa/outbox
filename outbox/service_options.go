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
	workers              int
	idleTime             time.Duration
	reserveFor           time.Duration
	reservationBatchSize int
	jobsRepo             JobsRepository
	fanoutJobsRepo       FanoutJobsRepository
	uniqueJobsRepo       UniqueJobsRepository
	uniqueBatchJobsRepo  UniqueBatchJobsRepository
	jobsStatRepo         JobsStatRepository
	jobsFailedRepo       JobsFailedRepository
	transactor           Transactor
	allowNonAtomicDLQ    bool
	logger               logger.Logger
}

func NewOptions(options ...OptOptionsSetter) (Options, error) {
	o := Options{
		workers:              1,
		idleTime:             time.Second,
		reserveFor:           5 * time.Minute,
		reservationBatchSize: 1,
		logger:               logger.WrapNamed(logger.Default(), serviceName),
		jobsRepo:             nil,
		jobsFailedRepo:       nil,
		transactor:           nil,
		allowNonAtomicDLQ:    false,
	}

	for _, opt := range options {
		opt(&o)
	}
	o.detectOptionalRepositories()

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
	if tc, ok := o.transactor.(TransactionCapabilities); !ok {
		if !o.allowNonAtomicDLQ {
			return ErrTransactionCapabilitiesRequired
		}
	} else if !tc.SupportsAtomicDLQ() && !o.allowNonAtomicDLQ {
		return ErrNonAtomicDLQUnsupported
	}
	if o.logger == nil {
		return errors.New("nil logger")
	}
	if o.workers < 1 {
		return fmt.Errorf("invalid number of workers: %d", o.workers)
	}
	if o.idleTime < 100*time.Millisecond || o.idleTime > 10*time.Second {
		return fmt.Errorf("invalid idle time: %s", o.idleTime)
	}
	if o.reserveFor < 1*time.Second || o.reserveFor > 10*time.Minute {
		return fmt.Errorf("invalid reserve for: %s", o.reserveFor)
	}
	return o.validateReservationBatch()
}

func (o *Options) validateReservationBatch() error {
	if o.reservationBatchSize < 1 || o.reservationBatchSize > MaxReservationBatchSize {
		return fmt.Errorf(
			"invalid reservation batch size: %d (must be between 1 and %d)",
			o.reservationBatchSize,
			MaxReservationBatchSize,
		)
	}
	repositoryMax := o.jobsRepo.MaxReservationBatchSize()
	if repositoryMax < 1 {
		return fmt.Errorf("invalid repository reservation batch maximum: %d", repositoryMax)
	}
	if o.reservationBatchSize > repositoryMax {
		return fmt.Errorf(
			"%w: requested %d, repository maximum %d",
			ErrReservationBatchSizeUnsupported,
			o.reservationBatchSize,
			repositoryMax,
		)
	}
	return nil
}

func (o *Options) detectOptionalRepositories() {
	if o.uniqueJobsRepo == nil {
		if repo, ok := o.jobsRepo.(UniqueJobsRepository); ok {
			o.uniqueJobsRepo = repo
		}
	}
	if o.uniqueBatchJobsRepo == nil {
		if repo, ok := o.jobsRepo.(UniqueBatchJobsRepository); ok {
			o.uniqueBatchJobsRepo = repo
		}
	}
	if o.jobsStatRepo == nil {
		if repo, ok := o.jobsRepo.(JobsStatRepository); ok {
			o.jobsStatRepo = repo
		}
	}
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

// WithReservationBatchSize sets the maximum number of immediately available
// jobs reserved by one worker claim. The default is one and valid values are
// between one and 1000.
func WithReservationBatchSize(size int) OptOptionsSetter {
	return func(o *Options) {
		o.reservationBatchSize = size
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

// WithAllowNonAtomicDLQ explicitly permits using a transactor that does not
// guarantee atomic DLQ transitions (e.g. Picodata best-effort transactor).
func WithAllowNonAtomicDLQ() OptOptionsSetter {
	return func(o *Options) {
		o.allowNonAtomicDLQ = true
	}
}

func WithJobsRepo(jobsRepo JobsRepository) OptOptionsSetter {
	return func(o *Options) {
		o.jobsRepo = jobsRepo
	}
}

// WithFanoutJobsRepo enables immutable fan-out source and delivery jobs.
// Unsupported delivery schemas remain pending because the required jobs
// repository claims only registered capabilities.
func WithFanoutJobsRepo(jobsRepo FanoutJobsRepository) OptOptionsSetter {
	return func(o *Options) {
		o.fanoutJobsRepo = jobsRepo
	}
}

// WithUniqueJobsRepo enables immutable idempotency keys for direct versioned
// puts. Standard jobs repositories are detected automatically; this option
// supports split repository compositions.
func WithUniqueJobsRepo(jobsRepo UniqueJobsRepository) OptOptionsSetter {
	return func(o *Options) {
		o.uniqueJobsRepo = jobsRepo
	}
}

// WithUniqueBatchJobsRepo configures atomic unique batch staging for split
// repository compositions. Standard SQL repositories are detected
// automatically from WithJobsRepo.
func WithUniqueBatchJobsRepo(jobsRepo UniqueBatchJobsRepository) OptOptionsSetter {
	return func(o *Options) {
		o.uniqueBatchJobsRepo = jobsRepo
	}
}

// WithJobsStatRepo configures optional queue stats provider.
// Standard jobs repositories are detected automatically; this option supports
// split repository compositions.
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
