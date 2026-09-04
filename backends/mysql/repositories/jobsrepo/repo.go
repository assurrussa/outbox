package jobsrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/backends/mysql/repositories"
	coreoutbox "github.com/assurrussa/outbox/outbox"
)

const (
	defaultTableName            = "jobs"
	defaultIdempotencyTableName = "outbox_job_idempotency_keys"
)

type Option func(*config)

type config struct {
	tableName            string
	idempotencyTableName string
}

// WithJobsTable sets a custom table name for active jobs.
func WithJobsTable(tableName string) Option {
	return func(c *config) {
		c.tableName = tableName
	}
}

// WithIdempotencyTable sets a custom table name for the idempotency keys registry.
func WithIdempotencyTable(tableName string) Option {
	return func(c *config) {
		c.idempotencyTableName = tableName
	}
}

type Repo struct {
	client               mysql.Client
	tableName            string
	idempotencyTableName string
}

var (
	_ coreoutbox.JobsRepository              = (*Repo)(nil)
	_ coreoutbox.JobsStatRepository          = (*Repo)(nil)
	_ coreoutbox.FanoutJobsRepository        = (*Repo)(nil)
	_ coreoutbox.UniqueJobsRepository        = (*Repo)(nil)
	_ coreoutbox.UniqueBatchJobsRepository   = (*Repo)(nil)
	_ coreoutbox.BatchJobsRepository         = (*Repo)(nil)
	_ coreoutbox.BoundedBatchJobsRepository  = (*Repo)(nil)
	_ coreoutbox.DeferJobsRepository         = (*Repo)(nil)
	_ coreoutbox.FanoutMaintenanceRepository = (*Repo)(nil)
)

func New(client mysql.Client, opts ...Option) (*Repo, error) {
	if client == nil {
		return nil, errors.New("mysql jobsrepo: client is nil")
	}

	cfg := config{
		tableName:            defaultTableName,
		idempotencyTableName: defaultIdempotencyTableName,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	quotedTable, err := repositories.ValidateAndQuoteTableName(cfg.tableName)
	if err != nil {
		return nil, fmt.Errorf("jobs table: %w", err)
	}

	quotedIdempTable, err := repositories.ValidateAndQuoteTableName(cfg.idempotencyTableName)
	if err != nil {
		return nil, fmt.Errorf("idempotency table: %w", err)
	}

	return &Repo{
		client:               client,
		tableName:            quotedTable,
		idempotencyTableName: quotedIdempTable,
	}, nil
}

func Must(client mysql.Client, opts ...Option) *Repo {
	repo, err := New(client, opts...)
	if err != nil {
		panic(fmt.Errorf("fatal mysql jobs repo: %w", err))
	}

	return repo
}
