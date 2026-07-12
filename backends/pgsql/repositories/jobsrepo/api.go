package jobsrepo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/georgysavva/scany/v2/pgxscan"

	"github.com/assurrussa/outbox/backends/pgsql/repositories"
	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	querybuilder "github.com/assurrussa/outbox/shared/query_builder"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	tableName         = "jobs"
	columnAvailableAt = "available_at"
	columnLeaseToken  = "lease_token"
	columnQueue       = "queue"
	columnReservedAt  = "reserved_at"
)

var columns = []string{
	"id", columnQueue, "name", "schema_version", "payload", "attempts", columnReservedAt, columnLeaseToken,
	"deduplication_key", columnAvailableAt, "created_at",
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
	set "attempts" = "attempts" + 1, "reserved_at" = $3, "lease_token" = $4
	from cte 
	where "cte"."id" = "j"."id" returning
		"j".id,
		"j".queue,
		"j".name,
		"j".schema_version,
		"j".payload,
		"j".attempts,
		"j".reserved_at,
		"j".lease_token,
		"j".deduplication_key,
		"j".available_at,
		"j".created_at;`

	var data models.Job
	err := r.pgsql.DB().ScanOne(ctx, op, &data, query, now, now, until, types.LeaseTokenNil)
	if err != nil {
		if pgxscan.NotFound(err) {
			return models.Job{}, errors.Join(err, sharederrors.ErrNoJobs)
		}

		return models.Job{}, fmt.Errorf("query context: %w", pgsql.ErrorTransform(err))
	}

	return data, nil
}

func (r *Repo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	return r.CreateJobVersioned(ctx, name, coreoutbox.DefaultSchemaVersion, payload, availableAt)
}

func (r *Repo) CreateJobVersioned(
	ctx context.Context,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	capability := coreoutbox.JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return types.JobIDNil, fmt.Errorf("validate capability: %w", err)
	}

	return r.Create(ctx, models.Job{
		ID:            types.NewJobID(),
		Queue:         columnQueue,
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		Attempts:      0,
		ReservedAt:    sql.NullTime{Valid: true, Time: availableAt},
		LeaseToken:    types.LeaseTokenNil,
		AvailableAt:   availableAt,
		CreatedAt:     time.Now(),
	})
}

func (r *Repo) CreateJobVersionedUnique(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	const op = "jobs.repo.CreateJobVersionedUnique"

	if deduplicationKey == "" {
		return types.JobIDNil, fmt.Errorf("%s: empty deduplication key", op)
	}
	capability := coreoutbox.JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return types.JobIDNil, fmt.Errorf("%s: validate capability: %w", op, err)
	}

	jobID := types.NewJobID()
	createdAt := time.Now().UTC()
	fingerprint := jobFingerprint(name, schemaVersion, payload, availableAt)

	query := `
	with key_row as (
		insert into outbox_job_idempotency_keys (
			deduplication_key, job_id, fingerprint, created_at
		) values ($1, $2, $3, $4)
		on conflict (deduplication_key) do update
		set deduplication_key = excluded.deduplication_key
		where outbox_job_idempotency_keys.fingerprint = excluded.fingerprint
		returning job_id
	), inserted_job as (
		insert into jobs (
			id, queue, name, schema_version, payload, attempts, reserved_at,
			lease_token, deduplication_key, available_at, created_at
		)
		select
			key_row.job_id, 'queue', $5, $6, $7, 0, $8,
			$9, $1, $8, $4
		from key_row
		where key_row.job_id = $2
		returning id
	)
	select job_id from key_row;`

	var storedJobID types.JobID
	err := r.pgsql.DB().ScanOne(
		ctx,
		op,
		&storedJobID,
		query,
		deduplicationKey,
		jobID,
		fingerprint,
		createdAt,
		name,
		schemaVersion,
		payload,
		availableAt,
		types.LeaseTokenNil,
	)
	if err != nil {
		if pgxscan.NotFound(err) {
			return types.JobIDNil, coreoutbox.ErrIdempotencyConflict
		}

		return types.JobIDNil, fmt.Errorf("%s: create unique job: %w", op, pgsql.ErrorTransform(err))
	}

	return storedJobID, nil
}

func jobFingerprint(
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) string {
	version := strconv.FormatInt(int64(schemaVersion), 10)
	available := availableAt.UTC().Format(time.RFC3339Nano)
	canonical := strconv.Itoa(len(name)) + ":" + name +
		strconv.Itoa(len(version)) + ":" + version +
		strconv.Itoa(len(payload)) + ":" + payload +
		strconv.Itoa(len(available)) + ":" + available
	digest := sha256.Sum256([]byte(canonical))

	return hex.EncodeToString(digest[:])
}

func (r *Repo) PruneJobIdempotencyKeys(
	ctx context.Context,
	before time.Time,
	limit int,
) (int64, error) {
	const op = "jobs.repo.PruneJobIdempotencyKeys"

	if limit < 1 || limit > 10_000 {
		return 0, fmt.Errorf("%s: limit must be between 1 and 10000", op)
	}

	query := `
	with candidates as (
		select registry.deduplication_key
		from outbox_job_idempotency_keys as registry
		where registry.created_at < $1
			and not exists (
				select 1 from jobs
				where jobs.deduplication_key = registry.deduplication_key
			)
		order by registry.created_at, registry.deduplication_key
		limit $2
		for update skip locked
	)
	delete from outbox_job_idempotency_keys as registry
	using candidates
	where registry.deduplication_key = candidates.deduplication_key;`

	result, err := r.pgsql.DB().Exec(ctx, op, query, before, limit)
	if err != nil {
		return 0, fmt.Errorf("%s: prune idempotency keys: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
}

func (r *Repo) Create(ctx context.Context, job models.Job) (types.JobID, error) {
	const op = "jobs.repo.Create"

	reservedAt := job.ReservedAt
	if !reservedAt.Valid {
		reservedAt = sql.NullTime{Valid: true, Time: job.AvailableAt}
	}
	if job.SchemaVersion <= 0 {
		job.SchemaVersion = coreoutbox.DefaultSchemaVersion
	}

	builder := querybuilder.BuilderDollar().
		Insert(tableName).
		Suffix("RETURNING id").
		SetMap(querybuilder.Eq{
			"id":                job.ID,
			columnQueue:         job.Queue,
			"name":              job.Name,
			"schema_version":    job.SchemaVersion,
			"payload":           job.Payload,
			"attempts":          job.Attempts,
			columnReservedAt:    reservedAt,
			columnLeaseToken:    job.LeaseToken,
			"deduplication_key": job.DeduplicationKey,
			columnAvailableAt:   job.AvailableAt,
			"created_at":        job.CreatedAt,
		})

	var lastID types.JobID
	if err := r.pgsql.DB().Getx(ctx, op, &lastID, builder); err != nil {
		return types.JobIDNil, fmt.Errorf("error creating: %w", pgsql.ErrorTransform(err))
	}

	return lastID, nil
}

func (r *Repo) FindAndReserveJobForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
) (models.Job, error) {
	const op = "jobs.repo.FindAndReserveJobForCapabilities"

	if err := leaseToken.Validate(); err != nil {
		return models.Job{}, fmt.Errorf("%s: invalid lease token: %w", op, err)
	}
	if len(capabilities) == 0 {
		return models.Job{}, sharederrors.ErrNoJobs
	}

	names := make([]string, 0, len(capabilities))
	versions := make([]int32, 0, len(capabilities))
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return models.Job{}, fmt.Errorf("%s: invalid capability: %w", op, err)
		}
		names = append(names, capability.Name)
		versions = append(versions, int32(capability.SchemaVersion))
	}

	query := `
	with requested(name, schema_version) as (
		select * from unnest($4::text[], $5::integer[])
	), cte as (
		select j.id
		from jobs as j
		join requested as r
			on r.name = j.name and r.schema_version = j.schema_version
		where j.available_at <= $1
			and (j.reserved_at is null or j.reserved_at <= $1)
		order by j.available_at, j.created_at, j.id
		limit 1
		for update of j skip locked
	)
	update jobs as j
	set attempts = attempts + 1,
		reserved_at = $2,
		lease_token = $3
	from cte
	where cte.id = j.id
	returning
		j.id,
		j.queue,
		j.name,
		j.schema_version,
		j.payload,
		j.attempts,
		j.reserved_at,
		j.lease_token,
		j.deduplication_key,
		j.available_at,
		j.created_at;`

	var data models.Job
	err := r.pgsql.DB().ScanOne(ctx, op, &data, query, now, until, leaseToken, names, versions)
	if err != nil {
		if pgxscan.NotFound(err) {
			return models.Job{}, errors.Join(err, sharederrors.ErrNoJobs)
		}

		return models.Job{}, fmt.Errorf("query context: %w", pgsql.ErrorTransform(err))
	}

	return data, nil
}

func (r *Repo) ExtendJobLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	const op = "jobs.repo.ExtendJobLease"

	if jobID.IsZero() {
		return 0, fmt.Errorf("%s: invalid id", op)
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("%s: invalid lease token: %w", op, err)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Update(tableName).
		Set(columnReservedAt, until).
		Where(squirrel.Eq{"id": jobID, columnLeaseToken: leaseToken}).
		Where(squirrel.Gt{columnReservedAt: now})

	result, err := r.pgsql.DB().Execx(ctx, op, sqlBuilder)
	if err != nil {
		return 0, fmt.Errorf("%s: extend lease: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
}

func (r *Repo) DeleteJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	const op = "jobs.repo.DeleteJobWithLease"

	if jobID.IsZero() {
		return 0, fmt.Errorf("%s: invalid id", op)
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("%s: invalid lease token: %w", op, err)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Delete(tableName).
		Where(squirrel.Eq{"id": jobID, columnLeaseToken: leaseToken}).
		Where(squirrel.Gt{columnReservedAt: now})

	result, err := r.pgsql.DB().Execx(ctx, op, sqlBuilder)
	if err != nil {
		return 0, fmt.Errorf("%s: delete leased job: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
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
		Where(squirrel.LtOrEq{columnAvailableAt: now}).
		Where(squirrel.Or{squirrel.Eq{columnReservedAt: nil}, squirrel.LtOrEq{columnReservedAt: now}})

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
		Where(squirrel.Gt{columnReservedAt: now})

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
