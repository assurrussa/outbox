package jobsrepo

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

func (r *Repo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	query := strings.Concate(`INSERT INTO %s (
	id, queue, name, payload, attempts, reserved_at, available_at, created_at
) VALUES (?, ?, ?, ?, 0, NULL, ?, ?);`, r.tableName)

	id := types.NewJobID()
	nowMS := time.Now().UTC().UnixMilli()
	availableAtMS := availableAt.UTC().UnixMilli()

	exec := r.executor(ctx)
	if _, err := exec.ExecContext(ctx, query, id, "queue", name, payload, availableAtMS, nowMS); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) FindAndReserveJob(ctx context.Context, now, until time.Time) (models.Job, error) {
	if tx := transaction.GetTx(ctx); tx != nil {
		return r.findAndReserveWithExecutor(ctx, tx, now, until)
	}

	conn, err := r.client.DB().Conn(ctx)
	if err != nil {
		return models.Job{}, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return models.Job{}, err
	}

	job, err := r.findAndReserveWithExecutor(ctx, conn, now, until)
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return models.Job{}, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return models.Job{}, err
	}

	return job, nil
}

func (r *Repo) findAndReserveWithExecutor(
	ctx context.Context,
	exec sqlExecutor,
	now time.Time,
	until time.Time,
) (models.Job, error) {
	queryRows := strings.Concate(`
SELECT id, queue, name, payload, attempts, reserved_at, available_at, created_at FROM %s
WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?)
ORDER BY available_at ASC, created_at ASC
LIMIT 10;
`, r.tableName)
	queryUpdate := strings.Concate(`
UPDATE %s
SET attempts = attempts + 1, reserved_at = ?
WHERE id = ? AND attempts = ? AND (reserved_at IS NULL OR reserved_at <= ?);
`, r.tableName)

	nowMS := now.UTC().UnixMilli()
	untilMS := until.UTC().UnixMilli()

	rows, err := exec.QueryContext(ctx, queryRows, nowMS, nowMS)
	if err != nil {
		return models.Job{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			job         models.Job
			reservedAt  sql.NullInt64
			availableMS int64
			createdMS   int64
		)

		if err := rows.Scan(
			&job.ID,
			&job.Queue,
			&job.Name,
			&job.Payload,
			&job.Attempts,
			&reservedAt,
			&availableMS,
			&createdMS,
		); err != nil {
			return models.Job{}, err
		}

		res, err := exec.ExecContext(ctx, queryUpdate, untilMS, job.ID, job.Attempts, nowMS)
		if err != nil {
			return models.Job{}, err
		}

		affected, err := res.RowsAffected()
		if err != nil {
			return models.Job{}, err
		}
		if affected == 0 {
			continue
		}

		job.Attempts++
		job.ReservedAt = sql.NullTime{Time: time.UnixMilli(untilMS).UTC(), Valid: true}
		job.AvailableAt = time.UnixMilli(availableMS).UTC()
		job.CreatedAt = time.UnixMilli(createdMS).UTC()

		return job, nil
	}

	if err := rows.Err(); err != nil {
		return models.Job{}, err
	}

	return models.Job{}, sharederrors.ErrNoJobs
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

	nowMS := now.UTC().UnixMilli()

	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, nowMS, nowMS).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *Repo) CountReserved(ctx context.Context, now time.Time) (int64, error) {
	query := strings.Concate(`SELECT COUNT(*) FROM %s WHERE reserved_at > ?;`, r.tableName)

	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, now.UTC().UnixMilli()).Scan(&count); err != nil {
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

	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC().UnixMilli())
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

func (r *Repo) executor(ctx context.Context) sqlExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (models.Job, error) {
	var (
		job         models.Job
		reservedAt  sql.NullInt64
		availableMS int64
		createdMS   int64
	)

	if err := row.Scan(
		&job.ID,
		&job.Queue,
		&job.Name,
		&job.Payload,
		&job.Attempts,
		&reservedAt,
		&availableMS,
		&createdMS,
	); err != nil {
		return models.Job{}, err
	}

	if reservedAt.Valid {
		job.ReservedAt = sql.NullTime{Time: time.UnixMilli(reservedAt.Int64).UTC(), Valid: true}
	}
	job.AvailableAt = time.UnixMilli(availableMS).UTC()
	job.CreatedAt = time.UnixMilli(createdMS).UTC()

	return job, nil
}
