package jobsfailedrepo

import (
	"context"
	"fmt"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/outbox/models"
)

// ListPage returns rows ordered by created_at DESC, id DESC. Pass nil for the
// first page, then copy CreatedAt and ID from the last returned row into before.
// An empty page ends traversal. Non-positive limits default to DefaultPageSize;
// limits above MaxPageSize return an error.
// This is a live listing, not a snapshot across calls.
func (r *Repo) ListPage(ctx context.Context, limit int, before *picodata.PageCursor) ([]models.JobFailed, error) {
	if limit <= 0 {
		limit = picodata.DefaultPageSize
	}
	if limit > picodata.MaxPageSize {
		return nil, fmt.Errorf("page limit must not exceed %d", picodata.MaxPageSize)
	}
	query := fmt.Sprintf("SELECT %s FROM %s", "id, job_id, queue, name, COALESCE(schema_version, 1), payload, reason, failed_at, created_at, connection, exception", r.tableName)
	var args []any
	if before != nil {
		query += " WHERE created_at < $1 OR (created_at = $1 AND id < $2)"
		args = []any{before.CreatedAt.UTC(), before.ID}
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT %d;", limit)
	rows, err := r.executor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.JobFailed, 0, limit)
	for rows.Next() {
		job, err := scanJobFailed(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}
