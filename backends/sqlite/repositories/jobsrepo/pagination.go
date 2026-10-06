package jobsrepo

import (
	"context"
	"fmt"
	stdstrings "strings"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/outbox/models"
)

// ListPage returns rows ordered by created_at DESC, id DESC. Pass nil for the
// first page, then copy CreatedAt and ID from the last returned row into before.
// An empty page ends traversal. Non-positive limits default to DefaultPageSize;
// limits above MaxPageSize return an error.
// This is a live listing, not a snapshot across calls.
func (r *Repo) ListPage(ctx context.Context, limit int, before *sqlite.PageCursor) ([]models.Job, error) {
	if limit <= 0 {
		limit = sqlite.DefaultPageSize
	}
	if limit > sqlite.MaxPageSize {
		return nil, fmt.Errorf("page limit must not exceed %d", sqlite.MaxPageSize)
	}
	query := fmt.Sprintf("SELECT %s FROM %s", stdstrings.Join(jobColumns, ", "), r.tableName)
	var args []any
	if before != nil {
		query += " WHERE created_at < ? OR (created_at = ? AND id < ?)"
		args = []any{before.CreatedAt.UTC().UnixMilli(), before.CreatedAt.UTC().UnixMilli(), before.ID}
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT %d;", limit)
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}
