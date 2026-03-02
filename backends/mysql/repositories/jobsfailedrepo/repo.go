package jobsfailedrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/shared/strings"
)

const tableName = "jobs_failed"

type Repo struct {
	client    mysql.Client
	tableName string
}

func New(client mysql.Client, tableNames ...string) (*Repo, error) {
	if client == nil {
		return nil, errors.New("mysql jobsfailedrepo: client is nil")
	}

	return &Repo{
		client:    client,
		tableName: strings.SelectFirst(tableName, tableNames...),
	}, nil
}

func Must(client mysql.Client, tableNames ...string) *Repo {
	repo, err := New(client, tableNames...)
	if err != nil {
		panic(fmt.Errorf("fatal mysql jobs failed repo: %w", err))
	}

	return repo
}
