package jobsrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/infrastructure/picodata"
	"github.com/assurrussa/outbox/shared/strings"
)

const tableName = "outbox_jobs"

type Repo struct {
	client    picodata.Client
	tableName string
}

func New(client picodata.Client, tableNames ...string) (*Repo, error) {
	if client == nil {
		return nil, errors.New("outbox_jobs_failed: client is nil")
	}

	return &Repo{
		client:    client,
		tableName: strings.SelectFirst(tableName, tableNames...),
	}, nil
}

func Must(client picodata.Client, tableNames ...string) *Repo {
	repo, err := New(client, tableNames...)
	if err != nil {
		panic(fmt.Errorf("fatal jobs failed repo: %w", err))
	}

	return repo
}
