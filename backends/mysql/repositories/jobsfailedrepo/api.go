package jobsfailedrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

func (r *Repo) CreateFailedJob(ctx context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error) {
	return r.CreateFailedJobVersioned(
		ctx, jobID, name, coreoutbox.DefaultSchemaVersion, payload, reason,
	)
}

func (r *Repo) CreateFailedJobVersioned(
	ctx context.Context,
	jobID types.JobID,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	reason string,
) (types.JobID, error) {
	capability := coreoutbox.JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return types.JobIDNil, fmt.Errorf("validate capability: %w", err)
	}
	query := strings.Concate(`
INSERT INTO %s (
	id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`, r.tableName)

	id := types.NewJobID()
	now := time.Now().UTC()
	queueName := "queue"
	connection := ""
	exception := ""

	exec := r.executor(ctx)
	if _, err := exec.ExecContext(
		ctx,
		query,
		id, jobID, queueName, name, schemaVersion, payload, reason,
		now, now, connection, exception,
	); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) Create(ctx context.Context, model models.JobFailed) (types.JobID, error) {
	query := strings.Concate(`
INSERT INTO %s (
	id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`, r.tableName)

	id := types.NewJobID()
	exec := r.executor(ctx)
	if _, err := exec.ExecContext(
		ctx,
		query,
		id, model.JobID, model.Queue, model.Name, normalizeSchemaVersion(model.SchemaVersion), model.Payload, model.Reason,
		model.FailedAt.UTC(), model.CreatedAt.UTC(), model.Connection, model.Exception,
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
SELECT id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception FROM %s WHERE id = ?;
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
SELECT id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception FROM %s
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
SELECT id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception FROM %s
ORDER BY created_at DESC LIMIT 100;
`, r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.JobFailed
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
SELECT id, job_id, queue, name, schema_version, payload, reason, failed_at, created_at, connection, exception FROM %%s
WHERE created_at < ?
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC())
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

func (r *Repo) executor(ctx context.Context) transaction.TxExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJobFailed(row scanner) (models.JobFailed, error) {
	var job models.JobFailed
	if err := row.Scan(
		&job.ID,
		&job.JobID,
		&job.Queue,
		&job.Name,
		&job.SchemaVersion,
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

func normalizeSchemaVersion(version coreoutbox.SchemaVersion) coreoutbox.SchemaVersion {
	if version <= 0 {
		return coreoutbox.DefaultSchemaVersion
	}
	return version
}
