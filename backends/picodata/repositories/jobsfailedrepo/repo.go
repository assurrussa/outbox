package jobsfailedrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/backends/picodata/repositories"
	coreoutbox "github.com/assurrussa/outbox/outbox"
)

const defaultTableName = "outbox_jobs_failed"

type Option func(*config)

type config struct {
	tableName string
}

// WithFailedJobsTable sets a custom table name for failed jobs.
func WithFailedJobsTable(tableName string) Option {
	return func(c *config) {
		c.tableName = tableName
	}
}

type Repo struct {
	client    picodata.Client
	tableName string
}

var _ coreoutbox.JobsFailedRepository = (*Repo)(nil)

func New(client picodata.Client, opts ...Option) (*Repo, error) {
	if client == nil {
		return nil, errors.New("picodata jobsfailedrepo: client is nil")
	}

	cfg := config{
		tableName: defaultTableName,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	quoted, err := repositories.ValidateAndQuoteTableName(cfg.tableName)
	if err != nil {
		return nil, err
	}

	return &Repo{
		client:    client,
		tableName: quoted,
	}, nil
}

func Must(client picodata.Client, opts ...Option) *Repo {
	repo, err := New(client, opts...)
	if err != nil {
		panic(fmt.Errorf("fatal jobs failed repo: %w", err))
	}

	return repo
}
