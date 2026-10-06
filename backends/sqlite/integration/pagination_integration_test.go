//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsrepo"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

func TestSQLiteListPage(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, count := range []int{12, 27} {
			t.Run(fmt.Sprintf("custom=%t/rows=%d", custom, count), func(t *testing.T) {
				testSQLiteListPage(t, custom, count)
			})
		}
	}
}

func testSQLiteListPage(t *testing.T, custom bool, count int) {
	t.Helper()
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)
	activeTable, failedTable := "jobs", "jobs_failed"
	active, failed := ts.jobsRepo, ts.jobsFailedRepo
	if custom {
		_, err := ts.db.DB().ExecContext(ctx, "ALTER TABLE jobs RENAME TO page_jobs")
		require.NoError(t, err)
		_, err = ts.db.DB().ExecContext(ctx, "ALTER TABLE jobs_failed RENAME TO page_failed")
		require.NoError(t, err)
		activeTable, failedTable = "page_jobs", "page_failed"
		active = jobsrepo.Must(ts.db, jobsrepo.WithJobsTable(activeTable))
		failed = jobsfailedrepo.Must(ts.db, jobsfailedrepo.WithFailedJobsTable(failedTable))
	}

	const precision = time.Millisecond
	base := time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC).Truncate(precision)
	expected := make([]sqlite.PageCursor, 0, count)
	// Insert out of cursor order. The first timestamp group alone exceeds a page.
	for i := range count {
		at := base.Add(2 * precision)
		if i >= 12 {
			at = base.Add(precision)
		}
		if i >= 23 {
			at = base
		}
		id := types.JobID(uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)))
		expected = append(expected, sqlite.PageCursor{CreatedAt: at, ID: id})
		storedID := id.String()
		if i%2 == 0 {
			storedID = strings.ToUpper(storedID)
		}

		_, err := ts.db.DB().ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
   (id, queue, name, schema_version, payload, attempts, available_at, created_at)
   VALUES (?, 'queue', 'page', 2, '{}', 0, ?, ?)`, activeTable), storedID, at.UnixMilli(), at.UnixMilli())
		require.NoError(t, err)
		_, err = ts.db.DB().ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
   (id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception)
   VALUES (?, ?, 'queue', 'page', 2, '{}', 'test', ?, ?, '', '')`, failedTable), storedID, types.NewJobID(), at.UnixMilli(), at.UnixMilli())
		require.NoError(t, err)
	}
	slices.SortFunc(expected, func(a, b sqlite.PageCursor) int {
		if n := b.CreatedAt.Compare(a.CreatedAt); n != 0 {
			return n
		}
		return -slices.Compare(a.ID[:], b.ID[:])
	})
	pages := map[string]func(context.Context, int, *sqlite.PageCursor) ([]sqlite.PageCursor, error){
		"active": func(ctx context.Context, limit int, cursor *sqlite.PageCursor) ([]sqlite.PageCursor, error) {
			rows, err := active.ListPage(ctx, limit, cursor)
			result := make([]sqlite.PageCursor, len(rows))
			for i, row := range rows {
				result[i] = sqlite.PageCursor{CreatedAt: row.CreatedAt, ID: row.ID}
			}
			return result, err
		},
		"failed": func(ctx context.Context, limit int, cursor *sqlite.PageCursor) ([]sqlite.PageCursor, error) {
			rows, err := failed.ListPage(ctx, limit, cursor)
			result := make([]sqlite.PageCursor, len(rows))
			for i, row := range rows {
				result[i] = sqlite.PageCursor{CreatedAt: row.CreatedAt, ID: row.ID}
			}
			return result, err
		},
	}
	for name, list := range pages {
		t.Run(name, func(t *testing.T) {
			for _, limit := range []int{10, 1, sqlite.MaxPageSize} {
				var cursor *sqlite.PageCursor
				seen := make([]sqlite.PageCursor, 0, count)
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
					cursor = new(sqlite.PageCursor)
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
				require.Equal(t, expected[:sqlite.DefaultPageSize], rows)
			}
			rows, err := list(ctx, sqlite.MaxPageSize+1, nil)
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

func TestSQLiteListPageZeroCreatedAt(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)
	expected := make([]types.JobID, 0, 12)
	for range 12 {
		// The existing public DLQ Create API accepts Go's zero timestamp.
		id, err := ts.jobsFailedRepo.Create(ctx, models.JobFailed{
			JobID: types.NewJobID(), Queue: "queue", Name: "historical", SchemaVersion: 1,
			Payload: "{}", Reason: "test", FailedAt: time.Now(), CreatedAt: time.Time{},
		})
		require.NoError(t, err)
		expected = append(expected, id)
	}
	slices.SortFunc(expected, func(a, b types.JobID) int { return -slices.Compare(a[:], b[:]) })
	var before *sqlite.PageCursor
	var seen []types.JobID
	for page := range 3 {
		rows, err := ts.jobsFailedRepo.ListPage(ctx, 10, before)
		require.NoError(t, err)
		require.Len(t, rows, []int{10, 2, 0}[page])
		for _, row := range rows {
			require.True(t, row.CreatedAt.IsZero())
			seen = append(seen, row.ID)
			before = &sqlite.PageCursor{CreatedAt: row.CreatedAt, ID: row.ID}
		}
	}
	require.Equal(t, expected, seen)
}

func TestSQLiteListPageZeroIDAndDeletedBoundary(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)
	at := time.Now().UTC().Truncate(time.Millisecond)
	// Raw historic data may contain a nil UUID; nil cursor, not nil UUID, is the first-page sentinel.
	for _, row := range []struct {
		id types.JobID
		at time.Time
	}{
		{types.JobIDNil, at}, {types.NewJobID(), at.Add(-time.Millisecond)},
	} {
		_, err := ts.db.DB().ExecContext(ctx, `INSERT INTO jobs (id, queue, name, payload, attempts, available_at, created_at)
   VALUES (?, 'queue', 'historical', '{}', 0, ?, ?)`, row.id, row.at.UnixMilli(), row.at.UnixMilli())
		require.NoError(t, err)
	}
	rows, err := ts.jobsRepo.ListPage(ctx, 1, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, types.JobIDNil, rows[0].ID)
	before := &sqlite.PageCursor{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}
	// Keyset continuation does not depend on the boundary row still existing.
	_, err = ts.db.DB().ExecContext(ctx, "DELETE FROM jobs WHERE id = ?", before.ID)
	require.NoError(t, err)
	rows, err = ts.jobsRepo.ListPage(ctx, 1, before)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].ID.IsZero())
	before = &sqlite.PageCursor{CreatedAt: rows[0].CreatedAt, ID: rows[0].ID}
	rows, err = ts.jobsRepo.ListPage(ctx, 1, before)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestSQLiteCaseAliasesHaveAmbiguousCursorIdentity(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)
	at := time.Now().UTC().Truncate(time.Millisecond)
	rawID := "aaaaaaaa-0000-4000-8000-000000000001"
	for _, spelling := range []string{rawID, strings.ToUpper(rawID)} {
		_, err := ts.db.DB().ExecContext(ctx, `INSERT INTO jobs (id, queue, name, payload, attempts, available_at, created_at)
   VALUES (?, 'queue', 'ambiguous-identity', '{}', 0, ?, ?)`, spelling, at.UnixMilli(), at.UnixMilli())
		require.NoError(t, err)
		_, err = ts.db.DB().ExecContext(ctx, `INSERT INTO jobs_failed (id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception)
   VALUES (?, ?, 'queue', 'ambiguous-identity', '{}', 'test', ?, ?, '', '')`, spelling, types.NewJobID(), at.UnixMilli(), at.UnixMilli())
		require.NoError(t, err)
	}
	active, err := ts.jobsRepo.ListPage(ctx, 10, nil)
	require.NoError(t, err)
	require.Len(t, active, 2)
	failed, err := ts.jobsFailedRepo.ListPage(ctx, 10, nil)
	require.NoError(t, err)
	require.Len(t, failed, 2)
	// Case aliases of one UUID are distinct TEXT keys but collapse in the public
	// model. They violate the documented unique logical UUID identity precondition;
	// no (CreatedAt, parsed ID) cursor can distinguish these physical rows.
	require.Equal(t, sqlite.PageCursor{CreatedAt: active[0].CreatedAt, ID: active[0].ID},
		sqlite.PageCursor{CreatedAt: active[1].CreatedAt, ID: active[1].ID})
	require.Equal(t, sqlite.PageCursor{CreatedAt: failed[0].CreatedAt, ID: failed[0].ID},
		sqlite.PageCursor{CreatedAt: failed[1].CreatedAt, ID: failed[1].ID})
}
