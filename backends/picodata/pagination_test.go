package picodata_test

import (
	"context"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsrepo"
)

type activePager interface {
	ListPage(ctx context.Context, limit int, before *struct {
		CreatedAt time.Time
		ID        types.JobID
	}) ([]models.Job, error)
	ListPaged(ctx context.Context, limit int, before time.Time) ([]models.Job, error)
}
type failedPager interface {
	ListPage(ctx context.Context, limit int, before *struct {
		CreatedAt time.Time
		ID        types.JobID
	}) ([]models.JobFailed, error)
	ListPaged(ctx context.Context, limit int, before time.Time) ([]models.JobFailed, error)
}

var (
	_ activePager = (*jobsrepo.Repo)(nil)
	_ failedPager = (*jobsfailedrepo.Repo)(nil)
)

func TestPageValidation(t *testing.T) {
	active, failed := new(jobsrepo.Repo), new(jobsfailedrepo.Repo)
	if _, err := active.ListPage(context.Background(), picodata.MaxPageSize+1, nil); err == nil {
		t.Fatal("active page must be bounded")
	}
	if _, err := failed.ListPage(context.Background(), picodata.MaxPageSize+1, nil); err == nil {
		t.Fatal("failed page must be bounded")
	}
}
