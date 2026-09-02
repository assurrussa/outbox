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
	pgsqltx "github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
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
		ReservedAt:    sql.NullTime{},
		LeaseToken:    types.LeaseTokenNil,
		AvailableAt:   availableAt.UTC(),
		CreatedAt:     time.Now().UTC(),
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
	result, err := r.CreateJobVersionedUniqueResult(
		ctx, deduplicationKey, name, schemaVersion, payload, availableAt,
	)
	return result.JobID, err
}

func (r *Repo) CreateJobVersionedUniqueResult(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (coreoutbox.UniquePutResult, error) {
	const op = "jobs.repo.CreateJobVersionedUnique"

	if deduplicationKey == "" {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("%s: empty deduplication key", op)
	}
	capability := coreoutbox.JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("%s: validate capability: %w", op, err)
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
			key_row.job_id, 'queue', $5, $6, $7, 0, null,
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
			return coreoutbox.UniquePutResult{}, coreoutbox.ErrIdempotencyConflict
		}

		return coreoutbox.UniquePutResult{}, fmt.Errorf(
			"%s: create unique job: %w", op, pgsql.ErrorTransform(err),
		)
	}

	return coreoutbox.UniquePutResult{
		JobID:   storedJobID,
		Created: storedJobID == jobID,
	}, nil
}

func (r *Repo) CreateJobVersionedUniqueBatch(
	ctx context.Context,
	items []coreoutbox.UniqueBatchPut,
) ([]coreoutbox.UniquePutResult, error) {
	const op = "jobs.repo.CreateJobVersionedUniqueBatch"
	if len(items) < 1 || len(items) > coreoutbox.MaxReservationBatchSize {
		return nil, fmt.Errorf("%s: item count must be between 1 and %d", op, coreoutbox.MaxReservationBatchSize)
	}

	ordinals := make([]int32, len(items))
	keys := make([]string, len(items))
	names := make([]string, len(items))
	versions := make([]int32, len(items))
	payloads := make([]string, len(items))
	available := make([]time.Time, len(items))
	jobIDs := make([]string, len(items))
	fingerprints := make([]string, len(items))
	seen := make(map[string]struct{}, len(items))
	createdAt := time.Now().UTC()
	for index, item := range items {
		if item.DeduplicationKey == "" {
			return nil, fmt.Errorf("%s: item %d has an empty deduplication key", op, index)
		}
		if _, duplicate := seen[item.DeduplicationKey]; duplicate {
			return nil, fmt.Errorf("%s: duplicate deduplication key %q", op, item.DeduplicationKey)
		}
		seen[item.DeduplicationKey] = struct{}{}
		if err := (coreoutbox.JobCapability{Name: item.Name, SchemaVersion: item.SchemaVersion}).Validate(); err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", op, index, err)
		}
		jobID := types.NewJobID()
		ordinals[index] = int32(index)
		keys[index] = item.DeduplicationKey
		names[index] = item.Name
		versions[index] = int32(item.SchemaVersion)
		payloads[index] = item.Payload
		available[index] = item.AvailableAt.UTC()
		jobIDs[index] = jobID.String()
		fingerprints[index] = jobFingerprint(item.Name, item.SchemaVersion, item.Payload, item.AvailableAt)
	}

	results := make([]coreoutbox.UniquePutResult, 0, len(items))
	manager := pgsqltx.New(r.pgsql.DB())
	err := manager.RunInTx(ctx, func(txCtx context.Context) error {
		query := `
		with input as (
			select * from unnest(
				$1::integer[], $2::text[], $3::text[], $4::integer[],
				$5::text[], $6::timestamptz[], $7::uuid[], $8::text[]
			) as value(ord, deduplication_key, name, schema_version, payload, available_at, new_job_id, fingerprint)
		), key_rows as (
			insert into outbox_job_idempotency_keys (deduplication_key, job_id, fingerprint, created_at)
			select deduplication_key, new_job_id, fingerprint, $9
			from input
			on conflict (deduplication_key) do update
			set deduplication_key = excluded.deduplication_key
			where outbox_job_idempotency_keys.fingerprint = excluded.fingerprint
			returning deduplication_key, job_id
		), inserted_jobs as (
			insert into jobs (
				id, queue, name, schema_version, payload, attempts, reserved_at,
				lease_token, deduplication_key, available_at, created_at
			)
			select
				key_rows.job_id, 'queue', input.name, input.schema_version, input.payload,
				0, null, $10, input.deduplication_key, input.available_at, $9
			from input
			join key_rows using (deduplication_key)
			where key_rows.job_id = input.new_job_id
			returning id
		)
		select key_rows.job_id, key_rows.job_id = input.new_job_id as created
		from input
		join key_rows using (deduplication_key)
		order by input.ord;`

		var rows []struct {
			JobID   types.JobID `db:"job_id"`
			Created bool        `db:"created"`
		}
		if err := r.pgsql.DB().ScanAll(
			txCtx,
			op,
			&rows,
			query,
			ordinals,
			keys,
			names,
			versions,
			payloads,
			available,
			jobIDs,
			fingerprints,
			createdAt,
			types.LeaseTokenNil,
		); err != nil {
			return fmt.Errorf("stage unique batch: %w", pgsql.ErrorTransform(err))
		}
		if len(rows) != len(items) {
			return coreoutbox.ErrIdempotencyConflict
		}
		results = results[:0]
		for _, row := range rows {
			results = append(results, coreoutbox.UniquePutResult{JobID: row.JobID, Created: row.Created})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return results, nil
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
			columnReservedAt:    job.ReservedAt,
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

func (r *Repo) FindAndReserveJobsForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	if len(capabilities) == 0 {
		return nil, sharederrors.ErrNoJobs
	}

	return r.findAndReserveJobs(ctx, now, until, leaseToken, capabilities, limit)
}

func (r *Repo) FindAndReserveJobsForCapability(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capability coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	return r.FindAndReserveJobsForCapabilities(ctx, now, until, leaseToken, []coreoutbox.JobCapability{capability}, limit)
}

func (r *Repo) MaxExecutionBatchSize() int { return coreoutbox.MaxReservationBatchSize }

func (r *Repo) findAndReserveJobs(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	const op = "jobs.repo.FindAndReserveJobs"

	if err := validateBatchRequest(leaseToken, limit); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	args := []any{now.UTC(), until.UTC(), leaseToken, limit}
	names := make([]string, 0, len(capabilities))
	versions := make([]int32, 0, len(capabilities))
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, fmt.Errorf("%s: invalid capability: %w", op, err)
		}
		names = append(names, capability.Name)
		versions = append(versions, int32(capability.SchemaVersion))
	}
	args = append(args, names, versions)

	query := `
	with requested(name, schema_version) as (
		select * from unnest($5::text[], $6::integer[])
	), candidates as (
		select j.id
		from jobs as j
		join requested as r
			on r.name = j.name and r.schema_version = j.schema_version
		where j.available_at <= $1
			and (j.reserved_at is null or j.reserved_at <= $1)
		order by j.available_at, j.created_at, j.id
		limit $4
		for update of j skip locked
	), updated as (
		update jobs as j
		set attempts = attempts + 1,
			reserved_at = $2,
			lease_token = $3
		from candidates
		where candidates.id = j.id
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
			j.created_at
	)
	select * from updated
	order by available_at, created_at, id;`

	var jobs []models.Job
	if err := r.pgsql.DB().ScanAll(ctx, op, &jobs, query, args...); err != nil {
		return nil, fmt.Errorf("%s: query context: %w", op, pgsql.ErrorTransform(err))
	}
	if len(jobs) == 0 {
		return nil, sharederrors.ErrNoJobs
	}

	return jobs, nil
}

func (r *Repo) ExtendJobLeases(
	ctx context.Context,
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	const op = "jobs.repo.ExtendJobLeases"

	ids, err := validateBatchLeaseRequest(jobIDs, leaseToken)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", op, err)
	}
	query := `update jobs
		set reserved_at = $4
		where id = any($1::uuid[])
			and lease_token = $2
			and reserved_at > $3;`
	result, err := r.pgsql.DB().Exec(ctx, op, query, ids, leaseToken, now.UTC(), until.UTC())
	if err != nil {
		return 0, fmt.Errorf("%s: extend leases: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
}

func (r *Repo) ReleaseUnstartedJobsWithLease(
	ctx context.Context,
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	const op = "jobs.repo.ReleaseUnstartedJobsWithLease"

	ids, err := validateBatchLeaseRequest(jobIDs, leaseToken)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", op, err)
	}
	query := `update jobs
		set attempts = attempts - 1,
			reserved_at = null,
			lease_token = $4
		where id = any($1::uuid[])
			and lease_token = $2
			and reserved_at > $3
			and attempts > 0;`
	result, err := r.pgsql.DB().Exec(
		ctx,
		op,
		query,
		ids,
		leaseToken,
		now.UTC(),
		types.LeaseTokenNil,
	)
	if err != nil {
		return 0, fmt.Errorf("%s: release leases: %w", op, pgsql.ErrorTransform(err))
	}

	return result.RowsAffected(), nil
}

func (r *Repo) ApplyBatchJobOutcomes(
	ctx context.Context,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	outcomes []coreoutbox.BatchJobOutcome,
) (int64, error) {
	const op = "jobs.repo.ApplyBatchJobOutcomes"
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("%s: invalid lease token: %w", op, err)
	}
	if len(outcomes) < 1 || len(outcomes) > coreoutbox.MaxReservationBatchSize {
		return 0, fmt.Errorf("%s: outcome count must be between 1 and %d", op, coreoutbox.MaxReservationBatchSize)
	}

	ids := make([]string, len(outcomes))
	kinds := make([]int16, len(outcomes))
	available := make([]time.Time, len(outcomes))
	seen := make(map[types.JobID]struct{}, len(outcomes))
	for index, outcome := range outcomes {
		if err := outcome.JobID.Validate(); err != nil {
			return 0, fmt.Errorf("%s: outcome %d: %w", op, index, err)
		}
		if _, duplicate := seen[outcome.JobID]; duplicate {
			return 0, fmt.Errorf("%s: duplicate JobID %s", op, outcome.JobID)
		}
		seen[outcome.JobID] = struct{}{}
		switch outcome.Kind {
		case coreoutbox.BatchJobOutcomeSuccess:
		case coreoutbox.BatchJobOutcomeRetry, coreoutbox.BatchJobOutcomeDefer:
			if outcome.AvailableAt.IsZero() {
				return 0, fmt.Errorf("%s: outcome %d has an empty availability time", op, index)
			}
		case coreoutbox.BatchJobOutcomeDLQ:
			if outcome.Reason == "" {
				return 0, fmt.Errorf("%s: outcome %d has an invalid DLQ record", op, index)
			}
		default:
			return 0, fmt.Errorf("%s: outcome %d has unknown kind %d", op, index, outcome.Kind)
		}
		ids[index] = outcome.JobID.String()
		kinds[index] = int16(outcome.Kind)
		available[index] = outcome.AvailableAt.UTC()
		if available[index].IsZero() {
			available[index] = now.UTC()
		}
	}

	query := `
	with input as (
		select * from unnest(
			$1::uuid[], $2::smallint[], $3::timestamptz[]
		) as value(job_id, kind, available_at)
	), owned as materialized (
		select j.id, input.kind, input.available_at as next_available_at
		from jobs as j
		join input on input.job_id = j.id
		where j.lease_token = $4 and j.reserved_at > $5
		for update of j
	), deleted as (
		delete from jobs as j
		using owned
		where j.id = owned.id and owned.kind in ($6, $7)
		returning j.id
	), updated as (
		update jobs as j
		set attempts = case when owned.kind = $8 then j.attempts - 1 else j.attempts end,
			available_at = owned.next_available_at,
			reserved_at = null,
			lease_token = $9
		from owned
		where j.id = owned.id and owned.kind in ($10, $8) and j.attempts > 0
		returning j.id
	)
	select (select count(*) from deleted) + (select count(*) from updated);`

	var affected int64
	manager := pgsqltx.New(r.pgsql.DB())
	if err := manager.RunInTx(ctx, func(txCtx context.Context) error {
		if err := r.pgsql.DB().ScanOne(
			txCtx,
			op,
			&affected,
			query,
			ids,
			kinds,
			available,
			leaseToken,
			now.UTC(),
			int16(coreoutbox.BatchJobOutcomeSuccess),
			int16(coreoutbox.BatchJobOutcomeDLQ),
			int16(coreoutbox.BatchJobOutcomeDefer),
			types.LeaseTokenNil,
			int16(coreoutbox.BatchJobOutcomeRetry),
		); err != nil {
			return err
		}
		if affected != int64(len(outcomes)) {
			return fmt.Errorf(
				"%w: finalized %d of %d batch jobs",
				coreoutbox.ErrLeaseLost,
				affected,
				len(outcomes),
			)
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("%s: apply outcomes: %w", op, pgsql.ErrorTransform(err))
	}
	return affected, nil
}

func (r *Repo) DeferJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	return r.ApplyBatchJobOutcomes(ctx, leaseToken, now, []coreoutbox.BatchJobOutcome{{
		JobID:       jobID,
		Kind:        coreoutbox.BatchJobOutcomeDefer,
		AvailableAt: availableAt,
	}})
}

func validateBatchRequest(leaseToken coreoutbox.LeaseToken, limit int) error {
	if err := leaseToken.Validate(); err != nil {
		return fmt.Errorf("invalid lease token: %w", err)
	}
	if limit < 1 || limit > coreoutbox.MaxReservationBatchSize {
		return fmt.Errorf(
			"limit must be between 1 and %d: %d",
			coreoutbox.MaxReservationBatchSize,
			limit,
		)
	}

	return nil
}

func validateBatchLeaseRequest(
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
) ([]string, error) {
	if err := leaseToken.Validate(); err != nil {
		return nil, fmt.Errorf("invalid lease token: %w", err)
	}
	if len(jobIDs) < 1 || len(jobIDs) > coreoutbox.MaxReservationBatchSize {
		return nil, fmt.Errorf(
			"job ID count must be between 1 and %d: %d",
			coreoutbox.MaxReservationBatchSize,
			len(jobIDs),
		)
	}
	ids := make([]string, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		if err := jobID.Validate(); err != nil {
			return nil, fmt.Errorf("invalid job ID: %w", err)
		}
		ids = append(ids, jobID.String())
	}

	return ids, nil
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

func (r *Repo) RescheduleJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	const op = "jobs.repo.RescheduleJobWithLease"

	if jobID.IsZero() {
		return 0, fmt.Errorf("%s: invalid id", op)
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("%s: invalid lease token: %w", op, err)
	}

	sqlBuilder := querybuilder.BuilderDollar().
		Update(tableName).
		Set(columnAvailableAt, availableAt.UTC()).
		Set(columnReservedAt, nil).
		Set(columnLeaseToken, types.LeaseTokenNil).
		Where(squirrel.Eq{"id": jobID, columnLeaseToken: leaseToken}).
		Where(squirrel.Gt{columnReservedAt: now.UTC()})

	result, err := r.pgsql.DB().Execx(ctx, op, sqlBuilder)
	if err != nil {
		return 0, fmt.Errorf("%s: reschedule leased job: %w", op, pgsql.ErrorTransform(err))
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

func (*Repo) MaxReservationBatchSize() int {
	return coreoutbox.MaxReservationBatchSize
}

func (r *Repo) GetQueueStats(
	ctx context.Context,
	observedAt time.Time,
) (coreoutbox.QueueStats, error) {
	const op = "jobs.repo.GetQueueStats"

	observedAt = observedAt.UTC()
	query := `select
		name,
		coalesce(schema_version, 1) as schema_version,
		count(*) as total,
		count(*) filter (
			where available_at <= $1
				and (reserved_at is null or reserved_at <= $1)
		) as available,
		count(*) filter (
			where reserved_at > $1
				and lease_token <> '00000000-0000-0000-0000-000000000000'
		) as processing,
		min(available_at) filter (
			where available_at <= $1
				and (reserved_at is null or reserved_at <= $1)
		) as oldest_available_at
	from jobs
	group by name, coalesce(schema_version, 1)
	order by name, schema_version;`
	type capabilityStatsRow struct {
		Name              string                   `db:"name"`
		SchemaVersion     coreoutbox.SchemaVersion `db:"schema_version"`
		Total             int64                    `db:"total"`
		Available         int64                    `db:"available"`
		Processing        int64                    `db:"processing"`
		OldestAvailableAt sql.NullTime             `db:"oldest_available_at"`
	}

	var rows []capabilityStatsRow
	if err := r.pgsql.DB().ScanAll(ctx, op, &rows, query, observedAt); err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf(
			"%s: aggregate active queue: %w",
			op,
			pgsql.ErrorTransform(err),
		)
	}

	stats := coreoutbox.QueueStats{
		ObservedAt:   observedAt,
		ByCapability: make([]coreoutbox.CapabilityQueueStats, 0, len(rows)),
	}
	for _, row := range rows {
		group := coreoutbox.CapabilityQueueStats{
			Name:          row.Name,
			SchemaVersion: row.SchemaVersion,
			Total:         row.Total,
			Available:     row.Available,
			Processing:    row.Processing,
		}
		if row.OldestAvailableAt.Valid {
			group.OldestAvailableAt = row.OldestAvailableAt.Time.UTC()
		}
		stats.Total += row.Total
		stats.Available += row.Available
		stats.Processing += row.Processing
		stats.ByCapability = append(stats.ByCapability, group)
	}

	return stats, nil
}
