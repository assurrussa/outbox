package jobsfailedrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/georgysavva/scany/v2/pgxscan"

	"github.com/assurrussa/outbox/backends/pgsql/repositories"
	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/outbox/models"
	querybuilder "github.com/assurrussa/outbox/shared/query_builder"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	tableName = "jobs_failed"
)

var columns = []string{
	"id", "job_id", "connection", "queue", "name", "payload", "reason", "exception", "failed_at", "created_at",
}

func (r *Repo) CreateFailedJob(
	ctx context.Context,
	jobID types.JobID,
	name string,
	payload string,
	reason string,
) (types.JobID, error) {
	return r.Create(ctx, models.JobFailed{
		ID:         types.NewJobID(),
		JobID:      jobID,
		Connection: "",
		Queue:      "queue",
		Name:       name,
		Payload:    payload,
		Reason:     reason,
		FailedAt:   time.Now(),
		CreatedAt:  time.Now(),
	})
}

func (r *Repo) Create(ctx context.Context, job models.JobFailed) (types.JobID, error) {
	const op = "jobs_failed.repo.Create"

	builder := querybuilder.BuilderDollar().
		Insert(tableName).
		Suffix("RETURNING id").
		SetMap(querybuilder.Eq{
			"id":         job.ID,
			"job_id":     job.JobID,
			"connection": job.Connection,
			"queue":      job.Queue,
			"name":       job.Name,
			"payload":    job.Payload,
			"reason":     job.Reason,
			"exception":  job.Exception,
			"failed_at":  job.FailedAt,
			"created_at": job.CreatedAt,
		})

	var lastID types.JobID
	if err := r.pgsql.DB().Getx(ctx, op, &lastID, builder); err != nil {
		return types.JobIDNil, fmt.Errorf("error creating: %w", pgsql.ErrorTransform(err))
	}

	return lastID, nil
}

func (r *Repo) All(ctx context.Context) ([]models.JobFailed, error) {
	const op = "jobs_failed.repo.All"

	sqlBuilder := querybuilder.BuilderDollar().
		Select(columns...).
		From(tableName).
		OrderBy("id desc").
		Limit(100)

	var data []models.JobFailed
	if err := r.pgsql.DB().ScanAllx(ctx, op, &data, sqlBuilder); err != nil {
		return nil, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return data, nil
}

func (r *Repo) GetByID(ctx context.Context, id types.JobID) (models.JobFailed, error) {
	const op = "jobs_failed.repo.GetByID"

	if id.IsZero() {
		return models.JobFailed{}, fmt.Errorf("%s: invalid id", op)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Select(columns...).
		From(tableName).
		Where(squirrel.Eq{"id": id}).
		Limit(1)

	var adm models.JobFailed
	if err := r.pgsql.DB().ScanOnex(ctx, op, &adm, sqlBuilder); err != nil {
		if pgxscan.NotFound(err) {
			return models.JobFailed{}, errors.Join(err, sharederrors.ErrNoJobs)
		}

		return models.JobFailed{}, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return adm, nil
}

func (r *Repo) CountLight(ctx context.Context) (int64, error) {
	const op = "jobs_failed.repo.CountLight"

	count, err := repositories.CountRowsForTable(ctx, r.pgsql.DB(), tableName)
	if err != nil {
		return 0, fmt.Errorf("%s: CountRowsForTable: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) CountExact(ctx context.Context) (int64, error) {
	const op = "jobs_failed.repo.CountExact"

	sqlBuilderCount := querybuilder.BuilderDollar().
		Select("count(id) as total").
		From(tableName)

	var count int64
	if err := r.pgsql.DB().ScanOnex(ctx, op, &count, sqlBuilderCount); err != nil {
		return 0, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) Delete(ctx context.Context, jobID types.JobID) (int64, error) {
	const op = "jobs_failed.repo.Delete"

	if jobID.IsZero() {
		return 0, fmt.Errorf("%s: invalid id", op)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Delete(tableName).
		Where(squirrel.Eq{"id": jobID})

	result, err := r.pgsql.DB().Execx(ctx, op, sqlBuilder)
	if err != nil {
		return 0, fmt.Errorf("%s: error deleted: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
}
