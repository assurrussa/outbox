package jobsfailedrepo

import (
	"errors"
	"fmt"

	pgsql "github.com/assurrussa/outbox/backends/pgsql"
)

type Options struct {
	pgsql pgsql.Client
}

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
