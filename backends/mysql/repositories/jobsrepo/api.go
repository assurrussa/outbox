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

const (
	defaultQueue              = "queue"
	batchCapabilityClaimIndex = "jobs_capability_claim_index"
)

var jobColumns = []string{
	"id", "queue", "name", "schema_version", "payload", "attempts", "reserved_at", "lease_token",
	"deduplication_key", "available_at", "created_at",
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

func (*Repo) MaxExecutionBatchSize() int { return coreoutbox.MaxReservationBatchSize }

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

	candidateQuery, candidateArgs := buildBatchCandidateQuery(
		r.tableName,
		now.UTC(),
		capabilities,
		limit,
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		jobIDs, err := r.findBatchCandidateIDs(ctx, candidateQuery, candidateArgs, limit)
		if err != nil {
			return nil, err
		}
		if len(jobIDs) == 0 {
			return nil, sharederrors.ErrNoJobs
		}

		jobs, claimed, err := r.reserveBatchCandidates(ctx, now.UTC(), until.UTC(), leaseToken, jobIDs)
		if err != nil {
			return nil, err
		}
		if claimed {
			return jobs, nil
		}
	}
}

func buildBatchCandidateQuery(
	tableName string,
	now time.Time,
	capabilities []coreoutbox.JobCapability,
	limit int,
) (string, []any) {
	uniqueCapabilities := make([]coreoutbox.JobCapability, 0, len(capabilities))
	seen := make(map[coreoutbox.JobCapability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		uniqueCapabilities = append(uniqueCapabilities, capability)
	}

	queries := make([]string, 0, len(uniqueCapabilities))
	args := make([]any, 0, len(uniqueCapabilities)*5+1)
	for _, capability := range uniqueCapabilities {
		queries = append(queries, fmt.Sprintf(`(SELECT id, available_at, created_at
			FROM %s FORCE INDEX (%s)
			WHERE name = ? AND schema_version = ?
				AND available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?)
			ORDER BY available_at, created_at, id
			LIMIT ?)`, tableName, batchCapabilityClaimIndex))
		args = append(args, capability.Name, capability.SchemaVersion, now, now, limit)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT id FROM (
		%s
	) AS candidates
	ORDER BY available_at, created_at, id
	LIMIT ?;`, stdstrings.Join(queries, "\n\t\tUNION ALL\n\t\t"))
	return query, args
}

func (r *Repo) findBatchCandidateIDs(
	ctx context.Context,
	query string,
	args []any,
	limit int,
) ([]types.JobID, error) {
	rows, err := r.client.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select batch claim candidates: %w", err)
	}

	jobIDs := make([]types.JobID, 0, limit)
	for rows.Next() {
		var jobID types.JobID
		if err := rows.Scan(&jobID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan batch claim candidate: %w", err)
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate batch claim candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close batch claim candidates: %w", err)
	}

	return jobIDs, nil
}

func (r *Repo) reserveBatchCandidates(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	jobIDs []types.JobID,
) ([]models.Job, bool, error) {
	tx, err := r.client.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, fmt.Errorf("begin batch claim tx: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	queryUpdate := fmt.Sprintf(`UPDATE %s FORCE INDEX (PRIMARY)
		SET attempts = attempts + 1, reserved_at = ?, lease_token = ?
		WHERE id IN (%s)
			AND available_at <= ? AND (reserved_at IS NULL OR reserved_at <= ?);`,
		r.tableName, sqlPlaceholders(len(jobIDs)))
	updateArgs := make([]any, 0, len(jobIDs)+4)
	updateArgs = append(updateArgs, until, leaseToken)
	for _, jobID := range jobIDs {
		updateArgs = append(updateArgs, jobID)
	}
	updateArgs = append(updateArgs, now, now)
	result, err := tx.ExecContext(ctx, queryUpdate, updateArgs...)
	if err != nil {
		rollback()
		return nil, false, fmt.Errorf("reserve batch claim candidates: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		rollback()
		return nil, false, fmt.Errorf("count reserved batch claim candidates: %w", err)
	}
	if updated == 0 {
		rollback()
		return nil, false, nil
	}
	if updated > int64(len(jobIDs)) {
		rollback()
		return nil, false, fmt.Errorf(
			"batch claim invariant: updated %d jobs from %d candidates",
			updated,
			len(jobIDs),
		)
	}

	queryClaimed := fmt.Sprintf(`SELECT %s FROM %s
		WHERE id IN (%s) AND lease_token = ?
		ORDER BY available_at, created_at, id;`,
		stdstrings.Join(jobColumns, ", "), r.tableName, sqlPlaceholders(len(jobIDs)))
	claimedArgs := make([]any, 0, len(jobIDs)+1)
	for _, jobID := range jobIDs {
		claimedArgs = append(claimedArgs, jobID)
	}
	claimedArgs = append(claimedArgs, leaseToken)
	rows, err := tx.QueryContext(ctx, queryClaimed, claimedArgs...)
	if err != nil {
		rollback()
		return nil, false, fmt.Errorf("load reserved batch claim candidates: %w", err)
	}

	jobs := make([]models.Job, 0, updated)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			rollback()
			return nil, false, fmt.Errorf("scan reserved batch claim candidate: %w", scanErr)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		rollback()
		return nil, false, fmt.Errorf("iterate reserved batch claim candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		rollback()
		return nil, false, fmt.Errorf("close reserved batch claim candidates: %w", err)
	}
	if int64(len(jobs)) != updated {
		rollback()
		return nil, false, fmt.Errorf(
			"batch claim invariant: loaded %d of %d updated jobs",
			len(jobs),
			updated,
		)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit batch claim tx: %w", err)
	}

	return jobs, true, nil
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
	args := []any{until.UTC()}
	for _, jobID := range jobIDs {
		args = append(args, jobID)
	}
	args = append(args, leaseToken, now.UTC())
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
	args = append(args, leaseToken, now.UTC())
	result, err := r.executor(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repo) ApplyBatchJobOutcomes(
	ctx context.Context,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	outcomes []coreoutbox.BatchJobOutcome,
) (int64, error) {
	if err := validateBatchOutcomes(leaseToken, outcomes); err != nil {
		return 0, err
	}
	var affected int64
	manager := transaction.New(r.client.DB())
	err := manager.RunInTx(ctx, func(txCtx context.Context) error {
		exec := r.executor(txCtx)
		deleted, err := r.deleteBatchTerminalOutcomes(txCtx, exec, leaseToken, now.UTC(), outcomes)
		if err != nil {
			return err
		}
		updated, err := r.updateBatchRetryOutcomes(txCtx, exec, leaseToken, now.UTC(), outcomes)
		if err != nil {
			return err
		}
		affected = deleted + updated
		if affected != int64(len(outcomes)) {
			return fmt.Errorf(
				"%w: finalized %d of %d batch jobs",
				coreoutbox.ErrLeaseLost,
				affected,
				len(outcomes),
			)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("apply batch job outcomes: %w", err)
	}
	return affected, nil
}

func (r *Repo) deleteBatchTerminalOutcomes(
	ctx context.Context,
	exec transaction.TxExecutor,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	outcomes []coreoutbox.BatchJobOutcome,
) (int64, error) {
	inputSQL, args := mysqlBatchOutcomeInput(outcomes)
	query := fmt.Sprintf(`DELETE job FROM %s AS job
		JOIN (%s) AS input ON input.job_id = job.id
		WHERE input.kind IN (?, ?) AND job.lease_token = ? AND job.reserved_at > ?;`, r.tableName, inputSQL)
	args = append(args,
		coreoutbox.BatchJobOutcomeSuccess,
		coreoutbox.BatchJobOutcomeDLQ,
		leaseToken,
		now,
	)
	result, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repo) updateBatchRetryOutcomes(
	ctx context.Context,
	exec transaction.TxExecutor,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	outcomes []coreoutbox.BatchJobOutcome,
) (int64, error) {
	inputSQL, args := mysqlBatchOutcomeInput(outcomes)
	query := fmt.Sprintf(`UPDATE %s AS job
		JOIN (%s) AS input ON input.job_id = job.id
		SET job.attempts = CASE WHEN input.kind = ? THEN job.attempts - 1 ELSE job.attempts END,
			job.available_at = input.available_at, job.reserved_at = NULL, job.lease_token = ?
		WHERE input.kind IN (?, ?) AND job.lease_token = ?
			AND job.reserved_at > ? AND job.attempts > 0;`, r.tableName, inputSQL)
	args = append(args,
		coreoutbox.BatchJobOutcomeDefer,
		types.LeaseTokenNil,
		coreoutbox.BatchJobOutcomeRetry,
		coreoutbox.BatchJobOutcomeDefer,
		leaseToken,
		now,
	)
	result, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func mysqlBatchOutcomeInput(outcomes []coreoutbox.BatchJobOutcome) (string, []any) {
	rows := make([]string, len(outcomes))
	args := make([]any, 0, len(outcomes)*3)
	for index, outcome := range outcomes {
		if index == 0 {
			rows[index] = "SELECT ? AS job_id, ? AS kind, ? AS available_at"
		} else {
			rows[index] = "SELECT ?, ?, ?"
		}
		args = append(args,
			outcome.JobID,
			outcome.Kind,
			outcome.AvailableAt.UTC(),
		)
	}
	return stdstrings.Join(rows, " UNION ALL "), args
}

func validateBatchOutcomes(leaseToken coreoutbox.LeaseToken, outcomes []coreoutbox.BatchJobOutcome) error {
	if err := leaseToken.Validate(); err != nil {
		return fmt.Errorf("invalid lease token: %w", err)
	}
	if len(outcomes) < 1 || len(outcomes) > coreoutbox.MaxReservationBatchSize {
		return fmt.Errorf("outcome count must be between 1 and %d", coreoutbox.MaxReservationBatchSize)
	}
	seen := make(map[types.JobID]struct{}, len(outcomes))
	for index, outcome := range outcomes {
		if err := outcome.JobID.Validate(); err != nil {
			return fmt.Errorf("outcome %d: %w", index, err)
		}
		if _, duplicate := seen[outcome.JobID]; duplicate {
			return fmt.Errorf("duplicate outcome JobID %s", outcome.JobID)
		}
		seen[outcome.JobID] = struct{}{}
		switch outcome.Kind {
		case coreoutbox.BatchJobOutcomeSuccess:
		case coreoutbox.BatchJobOutcomeRetry, coreoutbox.BatchJobOutcomeDefer:
			if outcome.AvailableAt.IsZero() {
				return fmt.Errorf("outcome %d has an empty availability time", index)
			}
		case coreoutbox.BatchJobOutcomeDLQ:
			if outcome.Reason == "" {
				return fmt.Errorf("outcome %d has an invalid DLQ record", index)
			}
		default:
			return fmt.Errorf("outcome %d has unknown kind %d", index, outcome.Kind)
		}
	}
	return nil
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

func (r *Repo) CreateJobVersionedUniqueBatch(
	ctx context.Context,
	items []coreoutbox.UniqueBatchPut,
) ([]coreoutbox.UniquePutResult, error) {
	if len(items) < 1 || len(items) > coreoutbox.MaxReservationBatchSize {
		return nil, fmt.Errorf("item count must be between 1 and %d", coreoutbox.MaxReservationBatchSize)
	}
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if item.DeduplicationKey == "" {
			return nil, fmt.Errorf("item %d has an empty deduplication key", index)
		}
		if _, duplicate := seen[item.DeduplicationKey]; duplicate {
			return nil, fmt.Errorf("duplicate deduplication key %q", item.DeduplicationKey)
		}
		seen[item.DeduplicationKey] = struct{}{}
		if err := (coreoutbox.JobCapability{Name: item.Name, SchemaVersion: item.SchemaVersion}).Validate(); err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
	}
	prepared := prepareUniqueBatchPuts(items, time.Now().UTC())
	results := make([]coreoutbox.UniquePutResult, 0, len(items))
	manager := transaction.New(r.client.DB())
	err := manager.RunInTx(ctx, func(txCtx context.Context) error {
		exec := r.executor(txCtx)
		stored, err := r.registerUniqueBatchKeys(txCtx, exec, prepared)
		if err != nil {
			return err
		}
		created := make([]preparedUniqueBatchPut, 0, len(prepared))
		for _, item := range prepared {
			key, ok := stored[item.put.DeduplicationKey]
			if !ok || key.fingerprint != item.fingerprint {
				return coreoutbox.ErrIdempotencyConflict
			}
			isCreated := key.jobID == item.jobID
			results = append(results, coreoutbox.UniquePutResult{JobID: key.jobID, Created: isCreated})
			if isCreated {
				created = append(created, item)
			}
		}
		return r.insertUniqueBatchJobs(txCtx, exec, created)
	})
	if err != nil {
		return nil, fmt.Errorf("create unique versioned job batch: %w", err)
	}
	return results, nil
}

type preparedUniqueBatchPut struct {
	put         coreoutbox.UniqueBatchPut
	jobID       types.JobID
	fingerprint string
	createdAt   time.Time
}

type storedUniqueBatchKey struct {
	jobID       types.JobID
	fingerprint string
}

func prepareUniqueBatchPuts(items []coreoutbox.UniqueBatchPut, createdAt time.Time) []preparedUniqueBatchPut {
	prepared := make([]preparedUniqueBatchPut, len(items))
	for index, item := range items {
		prepared[index] = preparedUniqueBatchPut{
			put:         item,
			jobID:       types.NewJobID(),
			fingerprint: jobFingerprint(item.Name, item.SchemaVersion, item.Payload, item.AvailableAt),
			createdAt:   createdAt,
		}
	}
	return prepared
}

func (r *Repo) registerUniqueBatchKeys(
	ctx context.Context,
	exec transaction.TxExecutor,
	prepared []preparedUniqueBatchPut,
) (map[string]storedUniqueBatchKey, error) {
	query := `INSERT INTO outbox_job_idempotency_keys
		(deduplication_key, job_id, fingerprint, created_at) VALUES ` +
		sqlValueRows(len(prepared), 4) +
		` ON DUPLICATE KEY UPDATE deduplication_key = VALUES(deduplication_key);`
	args := make([]any, 0, len(prepared)*4)
	for _, item := range prepared {
		args = append(args, item.put.DeduplicationKey, item.jobID, item.fingerprint, item.createdAt)
	}
	if _, err := exec.ExecContext(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("register batch idempotency keys: %w", err)
	}

	keys := make([]any, 0, len(prepared))
	for _, item := range prepared {
		keys = append(keys, item.put.DeduplicationKey)
	}
	rows, err := exec.QueryContext(ctx,
		`SELECT deduplication_key, job_id, fingerprint
		FROM outbox_job_idempotency_keys WHERE deduplication_key IN (`+sqlPlaceholders(len(prepared))+`);`,
		keys...,
	)
	if err != nil {
		return nil, fmt.Errorf("load batch idempotency keys: %w", err)
	}
	defer rows.Close()
	stored := make(map[string]storedUniqueBatchKey, len(prepared))
	for rows.Next() {
		var deduplicationKey string
		var key storedUniqueBatchKey
		if err := rows.Scan(&deduplicationKey, &key.jobID, &key.fingerprint); err != nil {
			return nil, fmt.Errorf("scan batch idempotency key: %w", err)
		}
		stored[deduplicationKey] = key
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate batch idempotency keys: %w", err)
	}
	return stored, nil
}

func (r *Repo) insertUniqueBatchJobs(
	ctx context.Context,
	exec transaction.TxExecutor,
	created []preparedUniqueBatchPut,
) error {
	if len(created) == 0 {
		return nil
	}
	query := sharedstrings.Concate(`INSERT INTO %s (
		id, queue, name, schema_version, payload, attempts, reserved_at,
		lease_token, deduplication_key, available_at, created_at
	) VALUES `, r.tableName) + sqlValueRows(len(created), 11) + ";"
	args := make([]any, 0, len(created)*11)
	for _, item := range created {
		args = append(args,
			item.jobID,
			defaultQueue,
			item.put.Name,
			item.put.SchemaVersion,
			item.put.Payload,
			0,
			nil,
			types.LeaseTokenNil,
			item.put.DeduplicationKey,
			item.put.AvailableAt.UTC(),
			item.createdAt,
		)
	}
	if _, err := exec.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("insert unique job batch: %w", err)
	}
	return nil
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

func (*Repo) MaxReservationBatchSize() int {
	return coreoutbox.MaxReservationBatchSize
}

func (r *Repo) GetQueueStats(
	ctx context.Context,
	observedAt time.Time,
) (coreoutbox.QueueStats, error) {
	observedAt = observedAt.UTC()
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
		observedAt,
		observedAt,
		observedAt,
		observedAt,
		observedAt,
	)
	if err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf("aggregate active queue: %w", err)
	}
	defer rows.Close()

	stats := coreoutbox.QueueStats{ObservedAt: observedAt}
	for rows.Next() {
		var (
			group  coreoutbox.CapabilityQueueStats
			oldest sql.NullTime
		)
		if err := rows.Scan(
			&group.Name,
			&group.SchemaVersion,
			&group.Total,
			&group.Available,
			&group.Processing,
			&oldest,
		); err != nil {
			return coreoutbox.QueueStats{}, fmt.Errorf("scan active queue aggregate: %w", err)
		}
		if oldest.Valid {
			group.OldestAvailableAt = oldest.Time.UTC()
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

func sqlValueRows(count, width int) string {
	row := "(" + sqlPlaceholders(width) + ")"
	rows := make([]string, count)
	for index := range rows {
		rows[index] = row
	}
	return stdstrings.Join(rows, ",")
}
