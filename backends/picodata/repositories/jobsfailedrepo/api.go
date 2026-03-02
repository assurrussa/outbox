package jobsfailedrepo

import (
	"context"
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

func (r *Repo) CreateFailedJob(ctx context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error) {
	query := strings.Concate(`
INSERT INTO %s (
    id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9);
`, r.tableName)

	id := types.NewJobID()
	queueName := "default"
	connection := ""
	exception := ""
	now := time.Now()

	exec := r.executor(ctx)
	if _, err := exec.Exec(ctx, query, id, jobID, queueName, name, payload, reason, now, connection, exception); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) Create(ctx context.Context, model models.JobFailed) (types.JobID, error) {
	query := strings.Concate(`
INSERT INTO %s (
    id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9);
`, r.tableName)

	id := types.NewJobID()
	exec := r.executor(ctx)
	if _, err := exec.Exec(ctx, query,
		id, model.JobID, model.Queue, model.Name, model.Payload, model.Reason,
		model.CreatedAt, model.Connection, model.Exception,
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
SELECT id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception FROM %s WHERE id = $1;
`, r.tableName)

	row := r.executor(ctx).QueryRow(ctx, query, jobID)

	job, err := scanJobFailed(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
WHERE job_id = $1
ORDER BY failed_at DESC
LIMIT 1;
`, r.tableName)

	row := r.executor(ctx).QueryRow(ctx, query, jobID)
	job, err := scanJobFailed(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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

	rows, err := r.executor(ctx).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.JobFailed
	for rows.Next() {
		var job models.JobFailed
		if err := rows.Scan(
			&job.ID,
			&job.JobID,
			&job.Queue,
			&job.Name,
			&job.Payload,
			&job.Reason,
			&job.FailedAt,
			&job.CreatedAt,
			&job.Connection,
			&job.Exception,
		); err != nil {
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
WHERE created_at < $1
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).Query(ctx, query, before)
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
	query := strings.Concate(`
DELETE FROM %s WHERE id = $1;
`, r.tableName)

	cmd, err := r.executor(ctx).Exec(ctx, query, jobID)
	if err != nil {
		return 0, err
	}

	return cmd.RowsAffected(), nil
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

func (r *Repo) executor(ctx context.Context) transaction.TxExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.Pool()
}

func scanJobFailed(row pgx.Row) (models.JobFailed, error) {
	var job models.JobFailed
	if err := row.Scan(
		&job.ID,
		&job.JobID,
		&job.Queue,
		&job.Name,
		&job.Payload,
		&job.Reason,
		&job.FailedAt,
		&job.CreatedAt,
		&job.Connection,
		&job.Exception,
	); err != nil {
		return models.JobFailed{}, err
	}

	return job, nil
}
