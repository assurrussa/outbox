package jobsrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/mysql"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/strings"
)

const tableName = "jobs"

type Repo struct {
	client    mysql.Client
	tableName string
}

var (
	_ coreoutbox.CapabilityJobsRepository    = (*Repo)(nil)
	_ coreoutbox.FanoutJobsRepository        = (*Repo)(nil)
	_ coreoutbox.FanoutMaintenanceRepository = (*Repo)(nil)
)

func New(client mysql.Client, tableNames ...string) (*Repo, error) {
	if client == nil {
		return nil, errors.New("mysql jobsrepo: client is nil")
	}

	return &Repo{
		client:    client,
		tableName: strings.SelectFirst(tableName, tableNames...),
	}, nil
}

func Must(client mysql.Client, tableNames ...string) *Repo {
	repo, err := New(client, tableNames...)
	if err != nil {
		panic(fmt.Errorf("fatal mysql jobs repo: %w", err))
	}

	return repo
}
