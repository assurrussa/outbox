package jobsrepo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	stdstrings "strings"
	"time"

	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	sharedstrings "github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

const defaultQueue = "queue"

var jobColumns = []string{
	"id", "queue", "name", "schema_version", "payload", "attempts", "reserved_at", "lease_token",
	"deduplication_key", "available_at", "created_at",
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
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
	id := types.NewJobID()
	query := sharedstrings.Concate(`INSERT INTO %s (
		id, queue, name, schema_version, payload, attempts, reserved_at,
		lease_token, deduplication_key, available_at, created_at
	) VALUES (?, ?, ?, ?, ?, 0, NULL, ?, NULL, ?, ?);`, r.tableName)
	if _, err := r.executor(ctx).ExecContext(
		ctx, query, id, defaultQueue, name, schemaVersion, payload, types.LeaseTokenNil,
		availableAt.UTC().UnixMilli(), time.Now().UTC().UnixMilli(),
	); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
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
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, fmt.Errorf("invalid capability: %w", err)
		}
	}

	return r.findAndReserveBatch(ctx, now, until, leaseToken, capabilities, limit)
}

func (r *Repo) findAndReserveBatch(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	if err := validateBatchClaim(leaseToken, limit); err != nil {
		return nil, err
	}
	if tx := transaction.GetTx(ctx); tx != nil {
		return r.findAndReserveBatchWithExecutor(ctx, tx, now, until, leaseToken, capabilities, limit)
	}

	conn, err := r.client.DB().Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return nil, err
	}
	jobs, err := r.findAndReserveBatchWithExecutor(ctx, conn, now, until, leaseToken, capabilities, limit)
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return nil, err
	}
	return jobs, nil
}

func (r *Repo) findAndReserveBatchWithExecutor(
	ctx context.Context,
	exec sqlExecutor,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	args := []any{now.UTC().UnixMilli(), now.UTC().UnixMilli()}
	clauses := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		clauses = append(clauses, "(name = ? AND schema_version = ?)")
		args = append(args, capability.Name, capability.SchemaVersion)
	}
	whereCapability := " AND (" + stdstrings.Join(clauses, " OR ") + ")"
	args = append(args, limit)
	querySelect := fmt.Sprintf(`SELECT %s FROM %s
		WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?)%s
		ORDER BY available_at, created_at, id LIMIT ?;`,
		stdstrings.Join(jobColumns, ", "), r.tableName, whereCapability,
	)
	rows, err := exec.QueryContext(ctx, querySelect, args...)
	if err != nil {
		return nil, err
	}
	jobs := make([]models.Job, 0, limit)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, sharederrors.ErrNoJobs
	}

	jobIDs := make([]types.JobID, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	queryUpdate := fmt.Sprintf(`UPDATE %s
		SET attempts = attempts + 1, reserved_at = ?, lease_token = ?
		WHERE id IN (%s);`, r.tableName, sqlPlaceholders(len(jobIDs)))
	updateArgs := []any{until.UTC().UnixMilli(), leaseToken}
	for _, jobID := range jobIDs {
		updateArgs = append(updateArgs, jobID)
	}
	result, err := exec.ExecContext(ctx, queryUpdate, updateArgs...)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != int64(len(jobs)) {
		return nil, sharederrors.ErrNoJobs
	}
	for index := range jobs {
		jobs[index].Attempts++
		jobs[index].ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
		jobs[index].LeaseToken = leaseToken
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
	if err := validateBatchLease(jobIDs, leaseToken); err != nil {
		return 0, err
	}
	query := fmt.Sprintf(`UPDATE %s SET reserved_at = ?
		WHERE id IN (%s) AND lease_token = ? AND reserved_at > ?;`, r.tableName, sqlPlaceholders(len(jobIDs)))
	args := []any{until.UTC().UnixMilli()}
	for _, jobID := range jobIDs {
		args = append(args, jobID)
	}
	args = append(args, leaseToken, now.UTC().UnixMilli())
	result, err := r.executor(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repo) ReleaseUnstartedJobsWithLease(
	ctx context.Context,
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	if err := validateBatchLease(jobIDs, leaseToken); err != nil {
		return 0, err
	}
	query := fmt.Sprintf(`UPDATE %s
		SET attempts = attempts - 1, reserved_at = NULL, lease_token = ?
		WHERE id IN (%s) AND lease_token = ? AND reserved_at > ? AND attempts > 0;`,
		r.tableName, sqlPlaceholders(len(jobIDs)))
	args := []any{types.LeaseTokenNil}
	for _, jobID := range jobIDs {
		args = append(args, jobID)
	}
	args = append(args, leaseToken, now.UTC().UnixMilli())
	result, err := r.executor(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repo) DeleteJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	if jobID.IsZero() {
		return 0, errors.New("invalid job id")
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("invalid lease token: %w", err)
	}
	query := sharedstrings.Concate(`DELETE FROM %s
		WHERE id = ? AND lease_token = ? AND reserved_at > ?;`, r.tableName)
	result, err := r.executor(ctx).ExecContext(ctx, query, jobID, leaseToken, now.UTC().UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repo) RescheduleJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	if jobID.IsZero() {
		return 0, errors.New("invalid job id")
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("invalid lease token: %w", err)
	}
	query := sharedstrings.Concate(`UPDATE %s
		SET available_at = ?, reserved_at = NULL, lease_token = ?
		WHERE id = ? AND lease_token = ? AND reserved_at > ?;`, r.tableName)
	result, err := r.executor(ctx).ExecContext(
		ctx,
		query,
		availableAt.UTC().UnixMilli(),
		types.LeaseTokenNil,
		jobID,
		leaseToken,
		now.UTC().UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
	if deduplicationKey == "" {
		return coreoutbox.UniquePutResult{}, errors.New("empty deduplication key")
	}
	capability := coreoutbox.JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("validate capability: %w", err)
	}
	if tx := transaction.GetTx(ctx); tx != nil {
		return r.createJobVersionedUnique(ctx, tx, deduplicationKey, name, schemaVersion, payload, availableAt)
	}
	conn, err := r.client.DB().Conn(ctx)
	if err != nil {
		return coreoutbox.UniquePutResult{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return coreoutbox.UniquePutResult{}, err
	}
	result, err := r.createJobVersionedUnique(
		ctx, conn, deduplicationKey, name, schemaVersion, payload, availableAt,
	)
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return coreoutbox.UniquePutResult{}, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return coreoutbox.UniquePutResult{}, err
	}

	return result, nil
}

func (r *Repo) createJobVersionedUnique(
	ctx context.Context,
	exec sqlExecutor,
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (coreoutbox.UniquePutResult, error) {
	jobID := types.NewJobID()
	createdMS := time.Now().UTC().UnixMilli()
	fingerprint := jobFingerprint(name, schemaVersion, payload, availableAt)
	result, err := exec.ExecContext(ctx, `INSERT INTO outbox_job_idempotency_keys
		(deduplication_key, job_id, fingerprint, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(deduplication_key) DO NOTHING;`,
		deduplicationKey, jobID, fingerprint, createdMS,
	)
	if err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("register idempotency key: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return coreoutbox.UniquePutResult{}, err
	}

	var storedJobID types.JobID
	var storedFingerprint string
	if err := exec.QueryRowContext(
		ctx,
		`SELECT job_id, fingerprint FROM outbox_job_idempotency_keys WHERE deduplication_key = ?;`,
		deduplicationKey,
	).Scan(&storedJobID, &storedFingerprint); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("read idempotency key: %w", err)
	}
	if storedFingerprint != fingerprint {
		return coreoutbox.UniquePutResult{}, coreoutbox.ErrIdempotencyConflict
	}
	if inserted == 0 {
		return coreoutbox.UniquePutResult{JobID: storedJobID, Created: false}, nil
	}

	insertJob := sharedstrings.Concate(`INSERT INTO %s (
		id, queue, name, schema_version, payload, attempts, reserved_at,
		lease_token, deduplication_key, available_at, created_at
	) VALUES (?, ?, ?, ?, ?, 0, NULL, ?, ?, ?, ?);`, r.tableName)
	if _, err := exec.ExecContext(
		ctx, insertJob, storedJobID, defaultQueue, name, schemaVersion, payload,
		types.LeaseTokenNil, deduplicationKey, availableAt.UTC().UnixMilli(), createdMS,
	); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("create idempotent job: %w", err)
	}

	return coreoutbox.UniquePutResult{JobID: storedJobID, Created: true}, nil
}

func (r *Repo) PruneJobIdempotencyKeys(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 10_000 {
		return 0, errors.New("limit must be between 1 and 10000")
	}
	query := `DELETE FROM outbox_job_idempotency_keys
		WHERE deduplication_key IN (
			SELECT registry.deduplication_key
			FROM outbox_job_idempotency_keys AS registry
			WHERE registry.created_at < ?
				AND NOT EXISTS (
					SELECT 1 FROM jobs
					WHERE jobs.deduplication_key = registry.deduplication_key
				)
			ORDER BY registry.created_at, registry.deduplication_key
			LIMIT ?
		);`
	result, err := r.executor(ctx).ExecContext(ctx, query, before.UTC().UnixMilli(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func jobFingerprint(name string, schemaVersion coreoutbox.SchemaVersion, payload string, availableAt time.Time) string {
	version := strconv.FormatInt(int64(schemaVersion), 10)
	available := availableAt.UTC().Format(time.RFC3339Nano)
	canonical := strconv.Itoa(len(name)) + ":" + name +
		strconv.Itoa(len(version)) + ":" + version +
		strconv.Itoa(len(payload)) + ":" + payload +
		strconv.Itoa(len(available)) + ":" + available
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.Job, error) {
	if jobID.IsZero() {
		return models.Job{}, errors.New("invalid job id")
	}
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?;`, stdstrings.Join(jobColumns, ", "), r.tableName)
	job, err := scanJob(r.executor(ctx).QueryRowContext(ctx, query, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.Job{}, sharederrors.ErrNoJobs
	}
	return job, err
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	query := fmt.Sprintf(
		`SELECT %s FROM %s ORDER BY created_at DESC LIMIT 100;`, stdstrings.Join(jobColumns, ", "), r.tableName,
	)
	rows, err := r.executor(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.Job, 0, 100)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (*Repo) MaxReservationBatchSize() int {
	return coreoutbox.MaxReservationBatchSize
}

func (r *Repo) GetQueueStats(
	ctx context.Context,
	observedAt time.Time,
) (coreoutbox.QueueStats, error) {
	observedAt = observedAt.UTC()
	observedMS := observedAt.UnixMilli()
	query := sharedstrings.Concate(`SELECT
		name,
		COALESCE(schema_version, 1) AS schema_version,
		COUNT(*) AS total,
		SUM(CASE
			WHEN available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?) THEN 1
			ELSE 0
		END) AS available,
		SUM(CASE
			WHEN reserved_at > ?
				AND lease_token <> '00000000-0000-0000-0000-000000000000' THEN 1
			ELSE 0
		END) AS processing,
		MIN(CASE
			WHEN available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?) THEN available_at
			ELSE NULL
		END) AS oldest_available_at
	FROM %s
	GROUP BY name, COALESCE(schema_version, 1)
	ORDER BY name, schema_version;`, r.tableName)
	rows, err := r.executor(ctx).QueryContext(
		ctx,
		query,
		observedMS,
		observedMS,
		observedMS,
		observedMS,
		observedMS,
	)
	if err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf("aggregate active queue: %w", err)
	}
	defer rows.Close()

	stats := coreoutbox.QueueStats{ObservedAt: observedAt}
	for rows.Next() {
		var (
			group    coreoutbox.CapabilityQueueStats
			oldestMS sql.NullInt64
		)
		if err := rows.Scan(
			&group.Name,
			&group.SchemaVersion,
			&group.Total,
			&group.Available,
			&group.Processing,
			&oldestMS,
		); err != nil {
			return coreoutbox.QueueStats{}, fmt.Errorf("scan active queue aggregate: %w", err)
		}
		if oldestMS.Valid {
			group.OldestAvailableAt = time.UnixMilli(oldestMS.Int64).UTC()
		}
		stats.Total += group.Total
		stats.Available += group.Available
		stats.Processing += group.Processing
		stats.ByCapability = append(stats.ByCapability, group)
	}
	if err := rows.Err(); err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf("iterate active queue aggregate: %w", err)
	}

	return stats, nil
}

func (r *Repo) ListPaged(ctx context.Context, limit int, before time.Time) ([]models.Job, error) {
	if limit <= 0 {
		limit = 10
	}
	query := fmt.Sprintf(`SELECT %s FROM %s
		WHERE created_at < ? ORDER BY created_at DESC LIMIT %d;`,
		stdstrings.Join(jobColumns, ", "), r.tableName, limit,
	)
	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.Job, 0, limit)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (r *Repo) executor(ctx context.Context) sqlExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (models.Job, error) {
	var (
		job         models.Job
		reservedAt  sql.NullInt64
		availableMS int64
		createdMS   int64
	)
	if err := row.Scan(
		&job.ID, &job.Queue, &job.Name, &job.SchemaVersion, &job.Payload, &job.Attempts,
		&reservedAt, &job.LeaseToken, &job.DeduplicationKey, &availableMS, &createdMS,
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

func validateBatchClaim(leaseToken coreoutbox.LeaseToken, limit int) error {
	if err := leaseToken.Validate(); err != nil {
		return fmt.Errorf("invalid lease token: %w", err)
	}
	if limit < 1 || limit > coreoutbox.MaxReservationBatchSize {
		return fmt.Errorf("limit must be between 1 and %d", coreoutbox.MaxReservationBatchSize)
	}
	return nil
}

func validateBatchLease(jobIDs []types.JobID, leaseToken coreoutbox.LeaseToken) error {
	if len(jobIDs) == 0 || len(jobIDs) > coreoutbox.MaxReservationBatchSize {
		return fmt.Errorf(
			"job ID count must be between 1 and %d",
			coreoutbox.MaxReservationBatchSize,
		)
	}
	for _, jobID := range jobIDs {
		if jobID.IsZero() {
			return errors.New("invalid job id")
		}
	}
	if err := leaseToken.Validate(); err != nil {
		return fmt.Errorf("invalid lease token: %w", err)
	}
	return nil
}

func sqlPlaceholders(count int) string {
	return stdstrings.TrimSuffix(stdstrings.Repeat("?,", count), ",")
}
