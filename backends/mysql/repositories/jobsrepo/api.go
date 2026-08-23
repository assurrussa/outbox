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

	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
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

	id := types.NewJobID()
	now := time.Now().UTC()
	query := sharedstrings.Concate(`INSERT INTO %s (
		id, queue, name, schema_version, payload, attempts, reserved_at,
		lease_token, deduplication_key, available_at, created_at
	) VALUES (?, ?, ?, ?, ?, 0, NULL, ?, NULL, ?, ?);`, r.tableName)
	if _, err := r.executor(ctx).ExecContext(
		ctx, query, id, defaultQueue, name, schemaVersion, payload,
		types.LeaseTokenNil, availableAt.UTC(), now,
	); err != nil {
		return types.JobIDNil, err
	}

	return id, nil
}

func (r *Repo) FindAndReserveJob(ctx context.Context, now, until time.Time) (models.Job, error) {
	return r.findAndReserve(ctx, now, until, types.LeaseTokenNil, nil)
}

func (r *Repo) FindAndReserveJobForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
) (models.Job, error) {
	if err := leaseToken.Validate(); err != nil {
		return models.Job{}, fmt.Errorf("invalid lease token: %w", err)
	}
	if len(capabilities) == 0 {
		return models.Job{}, sharederrors.ErrNoJobs
	}
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return models.Job{}, fmt.Errorf("invalid capability: %w", err)
		}
	}

	return r.findAndReserve(ctx, now, until, leaseToken, capabilities)
}

func (r *Repo) findAndReserve(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken types.LeaseToken,
	capabilities []coreoutbox.JobCapability,
) (models.Job, error) {
	whereCapability := ""
	args := []any{now.UTC(), now.UTC()}
	if capabilities != nil {
		clauses := make([]string, 0, len(capabilities))
		for _, capability := range capabilities {
			clauses = append(clauses, "(name = ? AND schema_version = ?)")
			args = append(args, capability.Name, capability.SchemaVersion)
		}
		whereCapability = " AND (" + stdstrings.Join(clauses, " OR ") + ")"
	}

	querySelect := fmt.Sprintf(`SELECT %s FROM %s
		WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?)%s
		ORDER BY available_at, created_at, id
		LIMIT 1 FOR UPDATE SKIP LOCKED;`, stdstrings.Join(jobColumns, ", "), r.tableName, whereCapability)
	queryUpdate := sharedstrings.Concate(`UPDATE %s
		SET attempts = attempts + 1, reserved_at = ?, lease_token = ?
		WHERE id = ?;`, r.tableName)

	tx, err := r.client.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return models.Job{}, fmt.Errorf("begin claim tx: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	job, err := scanJob(tx.QueryRowContext(ctx, querySelect, args...))
	if err != nil {
		rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return models.Job{}, sharederrors.ErrNoJobs
		}
		return models.Job{}, err
	}

	result, err := tx.ExecContext(ctx, queryUpdate, until.UTC(), leaseToken, job.ID)
	if err != nil {
		rollback()
		return models.Job{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		rollback()
		return models.Job{}, err
	}
	if affected != 1 {
		rollback()
		return models.Job{}, sharederrors.ErrNoJobs
	}
	if err := tx.Commit(); err != nil {
		return models.Job{}, fmt.Errorf("commit claim tx: %w", err)
	}

	job.Attempts++
	job.ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
	job.LeaseToken = leaseToken

	return job, nil
}

func (r *Repo) ExtendJobLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	if jobID.IsZero() {
		return 0, errors.New("invalid job id")
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("invalid lease token: %w", err)
	}
	query := sharedstrings.Concate(`UPDATE %s SET reserved_at = ?
		WHERE id = ? AND lease_token = ? AND reserved_at > ?;`, r.tableName)
	result, err := r.executor(ctx).ExecContext(ctx, query, until.UTC(), jobID, leaseToken, now.UTC())
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
	result, err := r.executor(ctx).ExecContext(ctx, query, jobID, leaseToken, now.UTC())
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
		availableAt.UTC(),
		types.LeaseTokenNil,
		jobID,
		leaseToken,
		now.UTC(),
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

	tx, err := r.client.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("begin idempotency tx: %w", err)
	}
	txCtx := transaction.WithTx(ctx, tx)
	result, err := r.createJobVersionedUnique(
		txCtx, tx, deduplicationKey, name, schemaVersion, payload, availableAt,
	)
	if err != nil {
		_ = tx.Rollback()
		return coreoutbox.UniquePutResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("commit idempotency tx: %w", err)
	}

	return result, nil
}

func (r *Repo) createJobVersionedUnique(
	ctx context.Context,
	exec transaction.TxExecutor,
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (coreoutbox.UniquePutResult, error) {
	jobID := types.NewJobID()
	createdAt := time.Now().UTC()
	fingerprint := jobFingerprint(name, schemaVersion, payload, availableAt)
	insertKey := `INSERT INTO outbox_job_idempotency_keys
		(deduplication_key, job_id, fingerprint, created_at)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE deduplication_key = deduplication_key;`
	if _, err := exec.ExecContext(ctx, insertKey, deduplicationKey, jobID, fingerprint, createdAt); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("register idempotency key: %w", err)
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
	if storedJobID != jobID {
		return coreoutbox.UniquePutResult{JobID: storedJobID, Created: false}, nil
	}

	insertJob := sharedstrings.Concate(`INSERT INTO %s (
		id, queue, name, schema_version, payload, attempts, reserved_at,
		lease_token, deduplication_key, available_at, created_at
	) VALUES (?, ?, ?, ?, ?, 0, NULL, ?, ?, ?, ?);`, r.tableName)
	if _, err := exec.ExecContext(
		ctx, insertJob, storedJobID, defaultQueue, name, schemaVersion, payload,
		types.LeaseTokenNil, deduplicationKey, availableAt.UTC(), createdAt,
	); err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("create idempotent job: %w", err)
	}

	return coreoutbox.UniquePutResult{JobID: storedJobID, Created: true}, nil
}

func (r *Repo) PruneJobIdempotencyKeys(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 10_000 {
		return 0, errors.New("limit must be between 1 and 10000")
	}
	query := `DELETE registry FROM outbox_job_idempotency_keys AS registry
		JOIN (
			SELECT deduplication_key FROM (
				SELECT candidate.deduplication_key
				FROM outbox_job_idempotency_keys AS candidate
				WHERE candidate.created_at < ?
					AND NOT EXISTS (
						SELECT 1 FROM jobs
						WHERE jobs.deduplication_key = candidate.deduplication_key
					)
				ORDER BY candidate.created_at, candidate.deduplication_key
				LIMIT ?
			) AS bounded
		) AS candidates
		ON candidates.deduplication_key = registry.deduplication_key;`
	result, err := r.executor(ctx).ExecContext(ctx, query, before.UTC(), limit)
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

func (r *Repo) DeleteJob(ctx context.Context, jobID types.JobID) (int64, error) {
	query := sharedstrings.Concate(`DELETE FROM %s WHERE id = ?;`, r.tableName)
	result, err := r.executor(ctx).ExecContext(ctx, query, jobID)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.Job, error) {
	if jobID.IsZero() {
		return models.Job{}, errors.New("invalid job id")
	}
	query := sharedstrings.Concate(
		`SELECT %s FROM %%s WHERE id = ?;`, stdstrings.Join(jobColumns, ", "),
	)
	query = sharedstrings.Concate(query, r.tableName)
	job, err := scanJob(r.executor(ctx).QueryRowContext(ctx, query, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.Job{}, sharederrors.ErrNoJobs
	}

	return job, err
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	query := sharedstrings.Concate(
		`SELECT %s FROM %%s ORDER BY created_at DESC LIMIT 100;`, stdstrings.Join(jobColumns, ", "),
	)
	query = sharedstrings.Concate(query, r.tableName)
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

func (r *Repo) CountLight(ctx context.Context) (int64, error) { return r.CountExact(ctx) }

func (r *Repo) Count(ctx context.Context) (int64, error) { return r.CountExact(ctx) }

func (r *Repo) CountExact(ctx context.Context) (int64, error) {
	query := sharedstrings.Concate(`SELECT COUNT(*) FROM %s;`, r.tableName)
	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repo) CountAvailable(ctx context.Context, now time.Time) (int64, error) {
	query := sharedstrings.Concate(`SELECT COUNT(*) FROM %s
		WHERE available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?);`, r.tableName)
	var count int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, now.UTC(), now.UTC()).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repo) CountReserved(ctx context.Context, now time.Time) (int64, error) {
	query := sharedstrings.Concate(`SELECT COUNT(*) FROM %s WHERE reserved_at > ?;`, r.tableName)
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
	query := fmt.Sprintf(
		`SELECT %s FROM %s WHERE created_at < ? ORDER BY created_at DESC LIMIT %d;`,
		stdstrings.Join(jobColumns, ", "), r.tableName, limit,
	)
	rows, err := r.executor(ctx).QueryContext(ctx, query, before.UTC())
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

func (r *Repo) executor(ctx context.Context) transaction.TxExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.DB()
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (models.Job, error) {
	var job models.Job
	if err := row.Scan(
		&job.ID, &job.Queue, &job.Name, &job.SchemaVersion, &job.Payload, &job.Attempts,
		&job.ReservedAt, &job.LeaseToken, &job.DeduplicationKey, &job.AvailableAt, &job.CreatedAt,
	); err != nil {
		return models.Job{}, err
	}
	return job, nil
}
