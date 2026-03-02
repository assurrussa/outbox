package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

func (r *Repo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	query := strings.Concate(`INSERT INTO %s (
	id, queue, name, payload, attempts, reserved_at, available_at, created_at
) VALUES (?, ?, ?, ?, 0, NULL, ?, ?);`, r.tableName)

	id := types.NewJobID()
	now := time.Now().UTC()

	exec := r.executor(ctx)
	if _, err := exec.ExecContext(ctx, query, id, "queue", name, payload, availableAt.UTC(), now); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) FindAndReserveJob(ctx context.Context, now, until time.Time) (models.Job, error) {
	querySelect := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at
FROM %s
WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?)
ORDER BY available_at ASC, created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;
`, r.tableName)
	queryUpdate := strings.Concate(`
UPDATE %s
SET attempts = attempts + 1, reserved_at = ?
WHERE id = ?;
`, r.tableName)

	tx, err := r.client.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return models.Job{}, fmt.Errorf("begin tx: %w", err)
	}

	job := models.Job{}
	if err := tx.QueryRowContext(ctx, querySelect, now.UTC(), now.UTC()).Scan(
		&job.ID,
		&job.Queue,
		&job.Name,
		&job.Payload,
		&job.Attempts,
		&job.ReservedAt,
		&job.AvailableAt,
		&job.CreatedAt,
	); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return models.Job{}, sharederrors.ErrNoJobs
		}
		return models.Job{}, err
	}

	res, err := tx.ExecContext(ctx, queryUpdate, until.UTC(), job.ID)
	if err != nil {
		_ = tx.Rollback()
		return models.Job{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return models.Job{}, err
	}
	if affected == 0 {
		_ = tx.Rollback()
		return models.Job{}, sharederrors.ErrNoJobs
	}

	if err := tx.Commit(); err != nil {
		return models.Job{}, fmt.Errorf("commit tx: %w", err)
	}

	job.Attempts++
	job.ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}

	return job, nil
}

func (r *Repo) DeleteJob(ctx context.Context, jobID types.JobID) (int64, error) {
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

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.Job, error) {
	const op = "jobs.repo.GetByID"

	if jobID.IsZero() {
		return models.Job{}, fmt.Errorf("%s: invalid id", op)
	}

	query := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %s WHERE id = ?;
`, r.tableName)

	row := r.executor(ctx).QueryRowContext(ctx, query, jobID)

	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Job{}, sharederrors.ErrNoJobs
		}
		return models.Job{}, err
	}

	return job, nil
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	query := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %s
ORDER BY created_at DESC LIMIT 100;
`, r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]models.Job, 0, 100)
	for rows.Next() {
		job, err := scanJob(rows)
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

func (r *Repo) CountAvailable(ctx context.Context, now time.Time) (int64, error) {
	query := strings.Concate(`
SELECT COUNT(*) FROM %s
WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?);
`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, now.UTC(), now.UTC()).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) CountReserved(ctx context.Context, now time.Time) (int64, error) {
	query := strings.Concate(`SELECT COUNT(*) FROM %s WHERE reserved_at > ?;`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, now.UTC()).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) ListPaged(ctx context.Context, limit int, before time.Time) ([]models.Job, error) {
	if limit <= 0 {
		limit = 10
	}

	query := strings.Concate(fmt.Sprintf(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %%s
WHERE created_at < ?
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC())
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

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *Repo) executor(ctx context.Context) transaction.TxExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (models.Job, error) {
	var job models.Job
	if err := row.Scan(
		&job.ID,
		&job.Queue,
		&job.Name,
		&job.Payload,
		&job.Attempts,
		&job.ReservedAt,
		&job.AvailableAt,
		&job.CreatedAt,
	); err != nil {
		return models.Job{}, err
	}

	return job, nil
}
