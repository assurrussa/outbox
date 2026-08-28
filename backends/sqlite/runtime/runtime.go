package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/sqlite/storage"
	sqlitetx "github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

type Config struct {
	DSN                  string
	Workers              int
	IdleTime             time.Duration
	ReserveFor           time.Duration
	ReservationBatchSize int
	Logger               logger.Logger
}

type Runtime struct {
	client     sqlite.Client
	service    *coreoutbox.Service
	jobs       *jobsrepo.Repo
	failed     *jobsfailedrepo.Repo
	transactor *sqlitetx.Manager
}

func Open(ctx context.Context, config Config) (*Runtime, error) {
	config.DSN = strings.TrimSpace(config.DSN)
	if config.DSN == "" {
		return nil, errors.New("outbox SQLite DSN is required")
	}
	if config.Logger == nil {
		config.Logger = logger.Default().Named("outbox-sqlite-runtime")
	}
	client, err := storage.Create(
		ctx,
		config.DSN,
		storage.WithLogger(config.Logger),
		storage.WithMaxOpenConns(1),
		storage.WithMaxIdleConns(1),
	)
	if err != nil {
		return nil, fmt.Errorf("open outbox SQLite client: %w", err)
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
	transactor := sqlitetx.New(client.DB())
	options := []coreoutbox.OptOptionsSetter{
		coreoutbox.WithJobsRepo(jobs),
		coreoutbox.WithFanoutJobsRepo(jobs),
		coreoutbox.WithJobsFailedRepo(failed),
		coreoutbox.WithTransactor(transactor),
		coreoutbox.WithLogger(config.Logger),
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
	service, err := coreoutbox.New(options...)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create outbox service: %w", err)
	}

	return &Runtime{client: client, service: service, jobs: jobs, failed: failed, transactor: transactor}, nil
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
		return errors.New("outbox SQLite runtime is not initialized")
	}
	return r.client.DB().PingContext(ctx)
}

func (r *Runtime) BeginDrain() {
	if r != nil && r.service != nil {
		r.service.BeginDrain()
	}
}

func (r *Runtime) Service() *coreoutbox.Service { return r.service }

func (r *Runtime) Client() sqlite.Client { return r.client }

func (r *Runtime) Jobs() *jobsrepo.Repo { return r.jobs }

func (r *Runtime) FailedJobs() *jobsfailedrepo.Repo { return r.failed }

func (r *Runtime) Transactor() *sqlitetx.Manager { return r.transactor }

func (r *Runtime) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
