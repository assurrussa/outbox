package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/georgysavva/scany/v2/pgxscan"

	"github.com/assurrussa/outbox/infrastructure/pgsql/repositories"
	pgsql "github.com/assurrussa/outbox/infrastructure/pgsql/storage"
	"github.com/assurrussa/outbox/outbox/models"
	querybuilder "github.com/assurrussa/outbox/shared/query_builder"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	tableName = "jobs"
)

var columns = []string{
	"id", "queue", "name", "payload", "attempts", "reserved_at", "available_at", "created_at",
}

func (r *Repo) FindAndReserveJob(ctx context.Context, now time.Time, until time.Time) (models.Job, error) {
	const op = "jobs.repo.FindAndReserveJob"

	query := `
	with cte as (
		select "id" from "jobs" 
		where "available_at" <= $1 
			and "reserved_at" <= $2
		limit 1 for update skip locked
	) 
	update "jobs" as "j" 
	set "attempts" = "attempts" + 1, "reserved_at" = $3 
	from cte 
	where "cte"."id" = "j"."id" returning
		"j".id,
		"j".queue,
		"j".name,
		"j".payload,
		"j".attempts;`

	var data models.Job
	err := r.pgsql.DB().ScanOne(ctx, op, &data, query, now, now, until)
	if err != nil {
		if pgxscan.NotFound(err) {
			return models.Job{}, errors.Join(err, sharederrors.ErrNoJobs)
		}

		return models.Job{}, fmt.Errorf("query context: %w", pgsql.ErrorTransform(err))
	}

	return data, nil
}

func (r *Repo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	return r.Create(ctx, models.Job{
		ID:          types.NewJobID(),
		Queue:       "queue",
		Name:        name,
		Payload:     payload,
		Attempts:    0,
		ReservedAt:  sql.NullTime{Valid: true, Time: availableAt},
		AvailableAt: availableAt,
		CreatedAt:   time.Now(),
	})
}

func (r *Repo) Create(ctx context.Context, job models.Job) (types.JobID, error) {
	const op = "jobs.repo.Create"

	reservedAt := job.ReservedAt
	if !reservedAt.Valid {
		reservedAt = sql.NullTime{Valid: true, Time: job.AvailableAt}
	}

	builder := querybuilder.BuilderDollar().
		Insert(tableName).
		Suffix("RETURNING id").
		SetMap(querybuilder.Eq{
			"id":           job.ID,
			"queue":        job.Queue,
			"name":         job.Name,
			"payload":      job.Payload,
			"attempts":     job.Attempts,
			"reserved_at":  reservedAt,
			"available_at": job.AvailableAt,
			"created_at":   job.CreatedAt,
		})

	var lastID types.JobID
	if err := r.pgsql.DB().Getx(ctx, op, &lastID, builder); err != nil {
		return types.JobIDNil, fmt.Errorf("error creating: %w", pgsql.ErrorTransform(err))
	}

	return lastID, nil
}

func (r *Repo) GetByID(ctx context.Context, id types.JobID) (models.Job, error) {
	const op = "jobs.repo.GetByID"

	if id.IsZero() {
		return models.Job{}, fmt.Errorf("%s: invalid id", op)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Select(columns...).
		From(tableName).
		Where(squirrel.Eq{"id": id}).
		Limit(1)

	var adm models.Job
	if err := r.pgsql.DB().ScanOnex(ctx, op, &adm, sqlBuilder); err != nil {
		if pgxscan.NotFound(err) {
			return models.Job{}, errors.Join(err, sharederrors.ErrNoJobs)
		}

		return models.Job{}, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return adm, nil
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	const op = "jobs.repo.All"

	sqlBuilder := querybuilder.BuilderDollar().
		Select(columns...).
		From(tableName).
		OrderBy("id desc").
		Limit(100)

	var data []models.Job
	if err := r.pgsql.DB().ScanAllx(ctx, op, &data, sqlBuilder); err != nil {
		return nil, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return data, nil
}

func (r *Repo) CountLight(ctx context.Context) (int64, error) {
	const op = "jobs.repo.CountLight"

	count, err := repositories.CountRowsForTable(ctx, r.pgsql.DB(), tableName)
	if err != nil {
		return 0, fmt.Errorf("%s: CountRowsForTable: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) CountExact(ctx context.Context) (int64, error) {
	const op = "jobs.repo.CountExact"

	sqlBuilderCount := querybuilder.BuilderDollar().
		Select("count(id) as total").
		From(tableName)

	var count int64
	if err := r.pgsql.DB().ScanOnex(ctx, op, &count, sqlBuilderCount); err != nil {
		return 0, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) CountAvailable(ctx context.Context, now time.Time) (int64, error) {
	const op = "jobs.repo.CountAvailable"

	sqlBuilderCount := querybuilder.BuilderDollar().
		Select("count(id) as total").
		From(tableName).
		Where(squirrel.LtOrEq{"available_at": now}).
		Where(squirrel.Or{squirrel.Eq{"available_at": nil}, squirrel.LtOrEq{"available_at": now}})

	var count int64
	if err := r.pgsql.DB().ScanOnex(ctx, op, &count, sqlBuilderCount); err != nil {
		return 0, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) CountReserved(ctx context.Context, now time.Time) (int64, error) {
	const op = "jobs.repo.CountReserved"

	sqlBuilderCount := querybuilder.BuilderDollar().
		Select("count(id) as total").
		From(tableName).
		Where(squirrel.Gt{"reserved_at": now})

	var count int64
	if err := r.pgsql.DB().ScanOnex(ctx, op, &count, sqlBuilderCount); err != nil {
		return 0, fmt.Errorf("%s: error get: %w", op, pgsql.ErrorTransform(err))
	}

	return count, nil
}

func (r *Repo) DeleteJob(ctx context.Context, jobID types.JobID) (int64, error) {
	const op = "jobs.repo.DeleteJob"

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
