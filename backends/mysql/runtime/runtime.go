package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/mysql/storage"
	mysqltx "github.com/assurrussa/outbox/backends/mysql/storage/transaction"
)

type Config struct {
	DSN                  string
	Workers              int
	IdleTime             time.Duration
	ReserveFor           time.Duration
	ReservationBatchSize int
	RetryPolicy          coreoutbox.RetryPolicy
	Observer             coreoutbox.Observer
	Logger               logger.Logger
}

type Runtime struct {
	client     mysql.Client
	service    *coreoutbox.Service
	jobs       *jobsrepo.Repo
	failed     *jobsfailedrepo.Repo
	transactor *mysqltx.Manager
}

func Open(ctx context.Context, config Config) (*Runtime, error) {
	config.DSN = strings.TrimSpace(config.DSN)
	if config.DSN == "" {
		return nil, errors.New("outbox MySQL DSN is required")
	}
	if config.Logger == nil {
		config.Logger = logger.Default().Named("outbox-mysql-runtime")
	}
	client, err := storage.Create(ctx, config.DSN, storage.WithLogger(config.Logger))
	if err != nil {
		return nil, fmt.Errorf("open outbox MySQL client: %w", err)
	}
	jobs, err := jobsrepo.New(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create outbox jobs repository: %w", err)
	}
	failed, err := jobsfailedrepo.New(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create outbox failed jobs repository: %w", err)
	}
	transactor := mysqltx.New(client.DB())
	service, err := newService(config, jobs, jobs, failed, transactor)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create outbox service: %w", err)
	}

	return &Runtime{client: client, service: service, jobs: jobs, failed: failed, transactor: transactor}, nil
}

func newService(
	config Config,
	jobs coreoutbox.JobsRepository,
	fanout coreoutbox.FanoutJobsRepository,
	failed coreoutbox.JobsFailedRepository,
	transactor coreoutbox.Transactor,
) (*coreoutbox.Service, error) {
	options := []coreoutbox.OptOptionsSetter{
		coreoutbox.WithJobsRepo(jobs),
		coreoutbox.WithJobsFailedRepo(failed),
		coreoutbox.WithTransactor(transactor),
		coreoutbox.WithLogger(config.Logger),
	}
	if fanout != nil {
		options = append(options, coreoutbox.WithFanoutJobsRepo(fanout))
	}
	if config.Workers != 0 {
		options = append(options, coreoutbox.WithWorkers(config.Workers))
	}
	if config.IdleTime != 0 {
		options = append(options, coreoutbox.WithIdleTime(config.IdleTime))
	}
	if config.ReserveFor != 0 {
		options = append(options, coreoutbox.WithReserveFor(config.ReserveFor))
	}
	if config.ReservationBatchSize != 0 {
		options = append(options, coreoutbox.WithReservationBatchSize(config.ReservationBatchSize))
	}
	if config.RetryPolicy != nil {
		options = append(options, coreoutbox.WithRetryPolicy(config.RetryPolicy))
	}
	if config.Observer != nil {
		options = append(options, coreoutbox.WithObserver(config.Observer))
	}
	return coreoutbox.New(options...)
}

func (r *Runtime) Run(ctx context.Context) error { return r.service.Run(ctx) }

func (r *Runtime) Readiness(ctx context.Context) error {
	if err := r.DatabaseReadiness(ctx); err != nil {
		return err
	}
	return r.service.Readiness(ctx)
}

func (r *Runtime) DatabaseReadiness(ctx context.Context) error {
	if r == nil || r.client == nil {
		return errors.New("outbox MySQL runtime is not initialized")
	}
	return r.client.DB().PingContext(ctx)
}

func (r *Runtime) BeginDrain() {
	if r != nil && r.service != nil {
		r.service.BeginDrain()
	}
}

func (r *Runtime) Service() *coreoutbox.Service { return r.service }

func (r *Runtime) Client() mysql.Client { return r.client }

func (r *Runtime) Jobs() *jobsrepo.Repo { return r.jobs }

func (r *Runtime) FailedJobs() *jobsfailedrepo.Repo { return r.failed }

func (r *Runtime) Transactor() *mysqltx.Manager { return r.transactor }

func (r *Runtime) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
