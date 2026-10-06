package mysql_test

import (
	"context"
	"testing"
	"time"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

type activePager interface {
	ListPage(context.Context, int, *struct {
		CreatedAt time.Time
		ID        types.JobID
	}) ([]models.Job, error)
	ListPaged(context.Context, int, time.Time) ([]models.Job, error)
}
type failedPager interface {
	ListPage(context.Context, int, *struct {
		CreatedAt time.Time
		ID        types.JobID
	}) ([]models.JobFailed, error)
	ListPaged(context.Context, int, time.Time) ([]models.JobFailed, error)
}

var (
	_ activePager = (*jobsrepo.Repo)(nil)
	_ failedPager = (*jobsfailedrepo.Repo)(nil)
)

func TestPageValidation(t *testing.T) {
	active, failed := new(jobsrepo.Repo), new(jobsfailedrepo.Repo)
	if _, err := active.ListPage(context.Background(), mysql.MaxPageSize+1, nil); err == nil {
		t.Fatal("active page must be bounded")
	}
	if _, err := failed.ListPage(context.Background(), mysql.MaxPageSize+1, nil); err == nil {
		t.Fatal("failed page must be bounded")
	}
}
