package jobsrepo

import (
	"errors"
	"fmt"

	pgsql "github.com/assurrussa/outbox/backends/pgsql"
	coreoutbox "github.com/assurrussa/outbox/outbox"
)

type Options struct {
	pgsql pgsql.Client
}

var (
	_ coreoutbox.JobsRepository              = (*Repo)(nil)
	_ coreoutbox.JobsStatRepository          = (*Repo)(nil)
	_ coreoutbox.FanoutJobsRepository        = (*Repo)(nil)
	_ coreoutbox.UniqueJobsRepository        = (*Repo)(nil)
	_ coreoutbox.FanoutMaintenanceRepository = (*Repo)(nil)
)

func NewOptions(pgsql pgsql.Client) Options {
	return Options{pgsql: pgsql}
}

type Repo struct {
	Options
}

func Must(opts Options) *Repo {
	repo, err := New(opts)
	if err != nil {
		panic(fmt.Errorf("fatal user repo: %w", err))
	}

	return repo
}

func New(opts Options) (*Repo, error) {
	if opts.pgsql == nil {
		return nil, errors.New("pgsql client is required")
	}

	return &Repo{opts}, nil
}
