package jobsrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/shared/strings"
)

const tableName = "jobs"

type Repo struct {
	client    sqlite.Client
	tableName string
}

func New(client sqlite.Client, tableNames ...string) (*Repo, error) {
	if client == nil {
		return nil, errors.New("sqlite jobsrepo: client is nil")
	}

	return &Repo{
		client:    client,
		tableName: strings.SelectFirst(tableName, tableNames...),
	}, nil
}

func Must(client sqlite.Client, tableNames ...string) *Repo {
	repo, err := New(client, tableNames...)
	if err != nil {
		panic(fmt.Errorf("fatal sqlite jobs repo: %w", err))
	}

	return repo
}
