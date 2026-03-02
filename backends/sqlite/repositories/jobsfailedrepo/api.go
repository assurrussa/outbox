package jobsfailedrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *Repo) CreateFailedJob(ctx context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error) {
	query := strings.Concate(`
INSERT INTO %s (
	id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`, r.tableName)

	id := types.NewJobID()
	nowMS := time.Now().UTC().UnixMilli()

	exec := r.executor(ctx)
	if _, err := exec.ExecContext(ctx, query, id, jobID, "queue", name, payload, reason, nowMS, nowMS, "", ""); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) Create(ctx context.Context, model models.JobFailed) (types.JobID, error) {
	query := strings.Concate(`
INSERT INTO %s (
	id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`, r.tableName)

	id := types.NewJobID()
	exec := r.executor(ctx)
	if _, err := exec.ExecContext(
		ctx,
		query,
		id,
		model.JobID,
		model.Queue,
		model.Name,
		model.Payload,
		model.Reason,
		model.FailedAt.UTC().UnixMilli(),
		model.CreatedAt.UTC().UnixMilli(),
		model.Connection,
		model.Exception,
	); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.JobFailed, error) {
	const op = "jobs_failed.repo.GetByID"

	if jobID.IsZero() {
		return models.JobFailed{}, fmt.Errorf("%s: invalid id", op)
	}

	query := strings.Concate(`
SELECT id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception FROM %s WHERE id = ?;
`, r.tableName)

	row := r.executor(ctx).QueryRowContext(ctx, query, jobID)

	job, err := scanJobFailed(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.JobFailed{}, sharederrors.ErrNoJobs
		}
		return models.JobFailed{}, err
	}

	return job, nil
}

func (r *Repo) FindByJobID(ctx context.Context, jobID types.JobID) (models.JobFailed, error) {
	const op = "jobs_failed.repo.FindByJobID"
	if jobID.IsZero() {
		return models.JobFailed{}, fmt.Errorf("%s: invalid id", op)
	}

	query := strings.Concate(`
SELECT id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception FROM %s
WHERE job_id = ?
ORDER BY failed_at DESC
LIMIT 1;
`, r.tableName)

	row := r.executor(ctx).QueryRowContext(ctx, query, jobID)
	job, err := scanJobFailed(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.JobFailed{}, sharederrors.ErrNoJobs
		}
		return models.JobFailed{}, err
	}

	return job, nil
}

func (r *Repo) All(ctx context.Context) ([]models.JobFailed, error) {
	query := strings.Concate(`
SELECT id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception FROM %s
ORDER BY created_at DESC LIMIT 100;
`, r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]models.JobFailed, 0, 100)
	for rows.Next() {
		job, err := scanJobFailed(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *Repo) ListPaged(ctx context.Context, limit int, before time.Time) ([]models.JobFailed, error) {
	if limit <= 0 {
		limit = 10
	}

	query := strings.Concate(fmt.Sprintf(`
SELECT id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception FROM %%s
WHERE created_at < ?
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC().UnixMilli())
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

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *Repo) Delete(ctx context.Context, jobID types.JobID) (int64, error) {
	query := strings.Concate(`DELETE FROM %s WHERE id = ?;`, r.tableName)

	cmd, err := r.executor(ctx).ExecContext(ctx, query, jobID)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := cmd.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rowsAffected, nil
}

func (r *Repo) CountLight(ctx context.Context) (int64, error) {
	return r.CountExact(ctx)
}

func (r *Repo) Count(ctx context.Context) (int64, error) {
	return r.CountExact(ctx)
}

func (r *Repo) CountExact(ctx context.Context) (int64, error) {
	query := strings.Concate(`SELECT COUNT(*) FROM %s;`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) executor(ctx context.Context) sqlExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJobFailed(row scanner) (models.JobFailed, error) {
	var (
		job       models.JobFailed
		failedMS  int64
		createdMS int64
	)

	if err := row.Scan(
		&job.ID,
		&job.JobID,
		&job.Queue,
		&job.Name,
		&job.Payload,
		&job.Reason,
		&failedMS,
		&createdMS,
		&job.Connection,
		&job.Exception,
	); err != nil {
		return models.JobFailed{}, err
	}

	job.FailedAt = time.UnixMilli(failedMS).UTC()
	job.CreatedAt = time.UnixMilli(createdMS).UTC()

	return job, nil
}
