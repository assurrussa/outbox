package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

func (r *Repo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	query := strings.Concate(`INSERT INTO %s (
	id, queue, name, payload, attempts, reserved_at, available_at, created_at
) VALUES ($1, $2, $3, $4, 0,  NULL, $5, $6);`, r.tableName)

	id := types.NewJobID()
	now := time.Now()
	queueName := "default"

	exec := r.executor(ctx)
	if _, err := exec.Exec(ctx, query, id, queueName, name, payload, availableAt, now); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) FindAndReserveJob(ctx context.Context, now, until time.Time) (models.Job, error) {
	queryRows := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %s
	WHERE available_at <= $1 AND (reserved_at IS NULL OR reserved_at <= $1) limit 10;
`, r.tableName)
	queryUpdate := strings.Concate(`
UPDATE %s
SET attempts = attempts + 1, reserved_at = $3
WHERE id = $1 and attempts = $2;
`, r.tableName)

	rows, err := r.executor(ctx).Query(ctx, queryRows, now)
	if err != nil {
		return models.Job{}, err
	}
	defer rows.Close()

	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return models.Job{}, err
		}

		job.ReservedAt = sql.NullTime{Time: until, Valid: true}
		job.Attempts++

		cmd, err := r.executor(ctx).Exec(ctx, queryUpdate, job.ID, job.Attempts-1, until)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return models.Job{}, sharederrors.ErrNoJobs
			}
			return models.Job{}, err
		}

		if cmd.RowsAffected() == 0 {
			continue
		}

		return job, nil
	}

	return models.Job{}, sharederrors.ErrNoJobs
}

func (r *Repo) DeleteJob(ctx context.Context, jobID types.JobID) (int64, error) {
	query := strings.Concate(`DELETE FROM %s WHERE id = $1;`, r.tableName)

	cmd, err := r.executor(ctx).Exec(ctx, query, jobID)
	if err != nil {
		return 0, err
	}

	return cmd.RowsAffected(), nil
}

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.Job, error) {
	const op = "jobs.repo.GetByID"

	if jobID.IsZero() {
		return models.Job{}, fmt.Errorf("%s: invalid id", op)
	}

	query := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %s WHERE id = $1;
`, r.tableName)

	row := r.executor(ctx).QueryRow(ctx, query, jobID)

	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Job{}, sharederrors.ErrNoJobs
		}
		return models.Job{}, err
	}

	return job, nil
}

func (r *Repo) CountLight(ctx context.Context) (int64, error) {
	return r.CountExact(ctx)
}

func (r *Repo) Count(ctx context.Context) (int64, error) {
	return r.CountExact(ctx)
}

func (r *Repo) CountExact(ctx context.Context) (int64, error) {
	query := strings.Concate(`
SELECT COUNT(*) FROM %s;
`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) CountAvailable(ctx context.Context, now time.Time) (int64, error) {
	query := strings.Concate(`
SELECT COUNT(*) FROM %s WHERE available_at <= $1 AND (reserved_at IS NULL OR reserved_at <= $1);
`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRow(ctx, query, now).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) CountReserved(ctx context.Context, now time.Time) (int64, error) {
	query := strings.Concate(`
SELECT COUNT(*) FROM %s WHERE reserved_at > $1;
`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRow(ctx, query, now).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	query := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at
FROM %s
ORDER BY created_at DESC
LIMIT 100;
`, r.tableName)

	rows, err := r.executor(ctx).Query(ctx, query)
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

func (r *Repo) ListPaged(ctx context.Context, limit int, before time.Time) ([]models.Job, error) {
	if limit <= 0 {
		limit = 10
	}

	query := strings.Concate(fmt.Sprintf(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at
FROM %%s
WHERE created_at < $1
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).Query(ctx, query, before)
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
	return r.client.Pool()
}

func scanJob(row pgx.Row) (models.Job, error) {
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
