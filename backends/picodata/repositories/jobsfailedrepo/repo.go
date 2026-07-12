package jobsfailedrepo

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/backends/picodata"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/strings"
)

const tableName = "outbox_jobs_failed"

type Repo struct {
	client    picodata.Client
	tableName string
}

var (
	_ coreoutbox.JobsFailedRepository           = (*Repo)(nil)
	_ coreoutbox.CapabilityJobsFailedRepository = (*Repo)(nil)
)

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
