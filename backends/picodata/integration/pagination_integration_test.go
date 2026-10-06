//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

func TestPicodataListPage(t *testing.T) {
	for _, count := range []int{12, 27} {
		t.Run(fmt.Sprintf("rows=%d", count), func(t *testing.T) {
			testPicodataListPage(t, count)
		})
	}
}

func testPicodataListPage(t *testing.T, count int) {
	t.Helper()
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	active, failed := ts.jobsRepo, ts.jobsFailedRepo
	// The Picodata helper isolates each test using custom table names.
	activeTable := ts.dbHelper.FnGetReplaceName("outbox_jobs")
	failedTable := ts.dbHelper.FnGetReplaceName("outbox_jobs_failed")
	const precision = time.Microsecond
	base := time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC).Truncate(precision)
	expected := make([]picodata.PageCursor, 0, count)
	// Insert out of cursor order. The first timestamp group alone exceeds a page.
	for i := range count {
		at := base.Add(2 * precision)
		if i >= 12 {
			at = base.Add(precision)
		}
		if i >= 23 {
			at = base
		}
		id := types.JobID(uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)))
		expected = append(expected, picodata.PageCursor{CreatedAt: at, ID: id})
		_, err := ts.db.Pool().Exec(ctx, fmt.Sprintf(`INSERT INTO %s
   (id, queue, name, schema_version, payload, attempts, available_at, created_at)
   VALUES ($1, 'queue', 'page', 2, '{}', 0, $2, $3)`, activeTable), id, at, at)
		require.NoError(t, err)
		_, err = ts.db.Pool().Exec(ctx, fmt.Sprintf(`INSERT INTO %s
   (id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception)
   VALUES ($1, $2, 'queue', 'page', 2, '{}', 'test', $3, $4, '', '')`, failedTable), id, types.NewJobID(), at, at)
		require.NoError(t, err)
	}
	slices.SortFunc(expected, func(a, b picodata.PageCursor) int {
		if n := b.CreatedAt.Compare(a.CreatedAt); n != 0 {
			return n
		}
		return -slices.Compare(a.ID[:], b.ID[:])
	})
	pages := map[string]func(context.Context, int, *picodata.PageCursor) ([]picodata.PageCursor, error){
		"active": func(ctx context.Context, limit int, cursor *picodata.PageCursor) ([]picodata.PageCursor, error) {
			rows, err := active.ListPage(ctx, limit, cursor)
			result := make([]picodata.PageCursor, len(rows))
			for i, row := range rows {
				result[i] = picodata.PageCursor{CreatedAt: row.CreatedAt, ID: row.ID}
			}
			return result, err
		},
		"failed": func(ctx context.Context, limit int, cursor *picodata.PageCursor) ([]picodata.PageCursor, error) {
			rows, err := failed.ListPage(ctx, limit, cursor)
			result := make([]picodata.PageCursor, len(rows))
			for i, row := range rows {
				result[i] = picodata.PageCursor{CreatedAt: row.CreatedAt, ID: row.ID}
			}
			return result, err
		},
	}
	for name, list := range pages {
		t.Run(name, func(t *testing.T) {
			for _, limit := range []int{10, 1, picodata.MaxPageSize} {
				var cursor *picodata.PageCursor
				seen := make([]picodata.PageCursor, 0, count)
				for page := 0; ; page++ {
					require.LessOrEqual(t, page, count, "pagination must terminate")
					rows, err := list(ctx, limit, cursor)
					require.NoError(t, err)
					require.LessOrEqual(t, len(rows), limit)
					if len(rows) == 0 {
						break
					}
					seen = append(seen, rows...)
					// A serialization round trip preserves backend timestamp precision.
					encoded, err := json.Marshal(rows[len(rows)-1])
					require.NoError(t, err)
					cursor = new(picodata.PageCursor)
					require.NoError(t, json.Unmarshal(encoded, cursor))
					cursor.CreatedAt = cursor.CreatedAt.In(time.FixedZone("offset", 3*60*60))
				}
				require.Equal(t, expected, seen, "no skips, duplicates, or ordering changes")
				rows, err := list(ctx, limit, cursor)
				require.NoError(t, err)
				require.Empty(t, rows, "end cursor stays empty")
			}
			for _, limit := range []int{0, -1} {
				rows, err := list(ctx, limit, nil)
				require.NoError(t, err)
				require.Equal(t, expected[:picodata.DefaultPageSize], rows)
			}
			rows, err := list(ctx, picodata.MaxPageSize+1, nil)
			require.Error(t, err)
			require.Empty(t, rows)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = list(cancelled, 10, nil)
			require.Error(t, err)
		})
	}
	// The deprecated timestamp-only method retains its exact strict-before API.
	oldActive, err := active.ListPaged(ctx, 10, base.Add(3*precision))
	require.NoError(t, err)
	require.Len(t, oldActive, 10)
	oldFailed, err := failed.ListPaged(ctx, 10, base.Add(3*precision))
	require.NoError(t, err)
	require.Len(t, oldFailed, 10)
	if count == 12 {
		var a []models.Job
		a, err = active.ListPaged(ctx, 10, oldActive[9].CreatedAt)
		require.NoError(t, err)
		require.Empty(t, a)
		var f []models.JobFailed
		f, err = failed.ListPaged(ctx, 10, oldFailed[9].CreatedAt)
		require.NoError(t, err)
		require.Empty(t, f)
	}
}
