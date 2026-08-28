package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	stdstrings "strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/strings"
	"github.com/assurrussa/outbox/shared/types"
)

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

	query := strings.Concate(`INSERT INTO %s (
	id, queue, name, schema_version, payload, attempts, reserved_at, lease_token, available_at, created_at
) VALUES ($1, $2, $3, $4, $5, 0, NULL, $6, $7, $8);`, r.tableName)

	id := types.NewJobID()
	now := time.Now()
	queueName := "default"

	exec := r.executor(ctx)
	if _, err := exec.Exec(
		ctx, query, id, queueName, name, schemaVersion, payload, types.LeaseTokenNil, availableAt, now,
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
	if limit != 1 {
		return nil, fmt.Errorf(
			"%w: Picodata supports reservation batch size 1, got %d",
			coreoutbox.ErrReservationBatchSizeUnsupported,
			limit,
		)
	}
	if err := leaseToken.Validate(); err != nil {
		return nil, fmt.Errorf("invalid lease token: %w", err)
	}
	if len(capabilities) == 0 {
		return nil, sharederrors.ErrNoJobs
	}
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, fmt.Errorf("invalid capability: %w", err)
		}
	}

	job, err := r.findAndReserveForCapabilities(ctx, now, until, leaseToken, capabilities)
	if err != nil {
		return nil, err
	}

	return []models.Job{job}, nil
}

func (r *Repo) findAndReserveForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken types.LeaseToken,
	capabilities []coreoutbox.JobCapability,
) (models.Job, error) {
	args := []any{now}
	clauses := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		namePosition := len(args) + 1
		versionPosition := namePosition + 1
		clauses = append(
			clauses,
			fmt.Sprintf(
				"(name = $%d AND COALESCE(schema_version, 1) = $%d)",
				namePosition,
				versionPosition,
			),
		)
		args = append(args, capability.Name, capability.SchemaVersion)
	}
	capabilityPredicate := " AND (" + stdstrings.Join(clauses, " OR ") + ")"
	queryRows := fmt.Sprintf(`
	SELECT id, queue, name, COALESCE(schema_version, 1), payload, attempts, reserved_at,
		COALESCE(lease_token, '00000000-0000-0000-0000-000000000000'), available_at, created_at
	FROM %s
	WHERE available_at <= $1 AND (reserved_at IS NULL OR reserved_at <= $1)%s
	ORDER BY available_at, created_at, id
	LIMIT 10;
`, r.tableName, capabilityPredicate)
	queryUpdate := strings.Concate(`
UPDATE %s
SET attempts = attempts + 1, reserved_at = $3, lease_token = $4
WHERE id = $1 AND attempts = $2 AND (reserved_at IS NULL OR reserved_at <= $5);
`, r.tableName)

	rows, err := r.executor(ctx).Query(ctx, queryRows, args...)
	if err != nil {
		return models.Job{}, err
	}

	candidates := make([]models.Job, 0, 10)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()

			return models.Job{}, err
		}
		candidates = append(candidates, job)
	}

	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return models.Job{}, rowsErr
	}

	// Picodata's pool-level API does not expose a connection-pinned
	// transaction. Release the query connection before attempting any CAS
	// update; otherwise workers can occupy every pool connection with open rows
	// and deadlock while each waits for another connection to run Exec.
	for _, job := range candidates {
		job.ReservedAt = sql.NullTime{Time: until, Valid: true}
		job.Attempts++

		cmd, err := r.executor(ctx).Exec(ctx, queryUpdate, job.ID, job.Attempts-1, until, leaseToken, now)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return models.Job{}, sharederrors.ErrNoJobs
			}
			return models.Job{}, err
		}

		if cmd.RowsAffected() == 0 {
			continue
		}

		job.LeaseToken = leaseToken

		return job, nil
	}

	return models.Job{}, sharederrors.ErrNoJobs
}

func (r *Repo) ExtendJobLeases(
	ctx context.Context,
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	jobID, err := validateSingleLeaseRequest(jobIDs, leaseToken)
	if err != nil {
		return 0, err
	}
	query := strings.Concate(`UPDATE %s SET reserved_at = $1
		WHERE id = $2 AND lease_token = $3 AND reserved_at > $4;`, r.tableName)
	result, err := r.executor(ctx).Exec(ctx, query, until.UTC(), jobID, leaseToken, now.UTC())
	if err != nil {
		return 0, err
	}

	return result.RowsAffected(), nil
}

func (r *Repo) ReleaseUnstartedJobsWithLease(
	ctx context.Context,
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	jobID, err := validateSingleLeaseRequest(jobIDs, leaseToken)
	if err != nil {
		return 0, err
	}
	query := strings.Concate(`UPDATE %s
		SET attempts = attempts - 1, reserved_at = NULL, lease_token = $1
		WHERE id = $2 AND lease_token = $3 AND reserved_at > $4 AND attempts > 0;`, r.tableName)
	result, err := r.executor(ctx).Exec(
		ctx,
		query,
		types.LeaseTokenNil,
		jobID,
		leaseToken,
		now.UTC(),
	)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected(), nil
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
	query := strings.Concate(`DELETE FROM %s
		WHERE id = $1 AND lease_token = $2 AND reserved_at > $3;`, r.tableName)
	result, err := r.executor(ctx).Exec(ctx, query, jobID, leaseToken, now)
	if err != nil {
		return 0, err
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
	if jobID.IsZero() {
		return 0, errors.New("invalid job id")
	}
	if err := leaseToken.Validate(); err != nil {
		return 0, fmt.Errorf("invalid lease token: %w", err)
	}
	query := strings.Concate(`UPDATE %s
		SET available_at = $1, reserved_at = NULL, lease_token = $2
		WHERE id = $3 AND lease_token = $4 AND reserved_at > $5;`, r.tableName)
	result, err := r.executor(ctx).Exec(
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

	return result.RowsAffected(), nil
}

func (r *Repo) GetByID(ctx context.Context, jobID types.JobID) (models.Job, error) {
	const op = "jobs.repo.GetByID"

	if jobID.IsZero() {
		return models.Job{}, fmt.Errorf("%s: invalid id", op)
	}

	query := strings.Concate(`
SELECT id, queue, name, COALESCE(schema_version, 1), payload, attempts, reserved_at,
    COALESCE(lease_token, '00000000-0000-0000-0000-000000000000'), available_at, created_at
FROM %s WHERE id = $1;
`, r.tableName)

	row := r.executor(ctx).QueryRow(ctx, query, jobID)

	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Job{}, sharederrors.ErrNoJobs
		}
		return models.Job{}, err
	}

	return job, nil
}

func (*Repo) MaxReservationBatchSize() int { return 1 }

func (r *Repo) GetQueueStats(
	ctx context.Context,
	observedAt time.Time,
) (coreoutbox.QueueStats, error) {
	observedAt = observedAt.UTC()
	query := strings.Concate(`
SELECT
	name,
	schema_version,
	CAST(COUNT(*) AS INT) AS total,
	CAST(SUM(CASE
		WHEN available_at <= $1 AND (reserved_at IS NULL OR reserved_at <= $1) THEN 1
		ELSE 0
	END) AS INT) AS available,
	CAST(SUM(CASE
		WHEN reserved_at > $1
			AND lease_token <> '00000000-0000-0000-0000-000000000000' THEN 1
		ELSE 0
	END) AS INT) AS processing,
	MIN(CASE
		WHEN available_at <= $1 AND (reserved_at IS NULL OR reserved_at <= $1)
			THEN CAST(available_at AS TEXT)
		ELSE CAST(NULL AS TEXT)
	END) AS oldest_available_at
FROM %s
GROUP BY name, schema_version
ORDER BY name, schema_version;
`, r.tableName)
	rows, err := r.executor(ctx).Query(ctx, query, observedAt)
	if err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf("aggregate active queue: %w", err)
	}
	defer rows.Close()

	stats := coreoutbox.QueueStats{ObservedAt: observedAt}
	groups := make(map[coreoutbox.JobCapability]coreoutbox.CapabilityQueueStats)
	for rows.Next() {
		var (
			group         coreoutbox.CapabilityQueueStats
			schemaVersion sql.NullInt64
			oldest        sql.NullString
		)
		if err := rows.Scan(
			&group.Name,
			&schemaVersion,
			&group.Total,
			&group.Available,
			&group.Processing,
			&oldest,
		); err != nil {
			return coreoutbox.QueueStats{}, fmt.Errorf("scan active queue aggregate: %w", err)
		}
		group.SchemaVersion = coreoutbox.DefaultSchemaVersion
		if schemaVersion.Valid && schemaVersion.Int64 > 0 {
			group.SchemaVersion = coreoutbox.SchemaVersion(schemaVersion.Int64)
		}
		if oldest.Valid {
			oldestAvailableAt, err := time.Parse(time.RFC3339Nano, oldest.String)
			if err != nil {
				return coreoutbox.QueueStats{}, fmt.Errorf(
					"parse oldest available time %q: %w",
					oldest.String,
					err,
				)
			}
			group.OldestAvailableAt = oldestAvailableAt.UTC()
		}
		stats.Total += group.Total
		stats.Available += group.Available
		stats.Processing += group.Processing

		capability := coreoutbox.JobCapability{
			Name: group.Name, SchemaVersion: group.SchemaVersion,
		}
		merged := groups[capability]
		merged.Name = group.Name
		merged.SchemaVersion = group.SchemaVersion
		merged.Total += group.Total
		merged.Available += group.Available
		merged.Processing += group.Processing
		if !group.OldestAvailableAt.IsZero() &&
			(merged.OldestAvailableAt.IsZero() || group.OldestAvailableAt.Before(merged.OldestAvailableAt)) {
			merged.OldestAvailableAt = group.OldestAvailableAt
		}
		groups[capability] = merged
	}
	if err := rows.Err(); err != nil {
		return coreoutbox.QueueStats{}, fmt.Errorf("iterate active queue aggregate: %w", err)
	}

	stats.ByCapability = make([]coreoutbox.CapabilityQueueStats, 0, len(groups))
	for _, group := range groups {
		stats.ByCapability = append(stats.ByCapability, group)
	}
	sort.Slice(stats.ByCapability, func(i, j int) bool {
		if stats.ByCapability[i].Name == stats.ByCapability[j].Name {
			return stats.ByCapability[i].SchemaVersion < stats.ByCapability[j].SchemaVersion
		}
		return stats.ByCapability[i].Name < stats.ByCapability[j].Name
	})

	return stats, nil
}

func (r *Repo) All(ctx context.Context) ([]models.Job, error) {
	query := strings.Concate(`
SELECT id, queue, name, COALESCE(schema_version, 1), payload, attempts, reserved_at,
    COALESCE(lease_token, '00000000-0000-0000-0000-000000000000'), available_at, created_at
FROM %s
ORDER BY created_at DESC
LIMIT 100;
`, r.tableName)

	rows, err := r.executor(ctx).Query(ctx, query)
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

func (r *Repo) ListPaged(ctx context.Context, limit int, before time.Time) ([]models.Job, error) {
	if limit <= 0 {
		limit = 10
	}

	query := strings.Concate(fmt.Sprintf(`
SELECT id, queue, name, COALESCE(schema_version, 1), payload, attempts, reserved_at,
    COALESCE(lease_token, '00000000-0000-0000-0000-000000000000'), available_at, created_at
FROM %%s
WHERE created_at < $1
ORDER BY created_at DESC
LIMIT %d;
`, limit), r.tableName)

	rows, err := r.executor(ctx).Query(ctx, query, before)
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

func validateSingleLeaseRequest(
	jobIDs []types.JobID,
	leaseToken coreoutbox.LeaseToken,
) (types.JobID, error) {
	if len(jobIDs) != 1 {
		return types.JobIDNil, fmt.Errorf(
			"%w: Picodata supports exactly one leased job, got %d",
			coreoutbox.ErrReservationBatchSizeUnsupported,
			len(jobIDs),
		)
	}
	if jobIDs[0].IsZero() {
		return types.JobIDNil, errors.New("invalid job id")
	}
	if err := leaseToken.Validate(); err != nil {
		return types.JobIDNil, fmt.Errorf("invalid lease token: %w", err)
	}

	return jobIDs[0], nil
}

func (r *Repo) executor(ctx context.Context) transaction.TxExecutor {
	if tx := transaction.GetTx(ctx); tx != nil {
		return tx
	}
	return r.client.Pool()
}

func scanJob(row pgx.Row) (models.Job, error) {
	var job models.Job
	if err := row.Scan(
		&job.ID,
		&job.Queue,
		&job.Name,
		&job.SchemaVersion,
		&job.Payload,
		&job.Attempts,
		&job.ReservedAt,
		&job.LeaseToken,
		&job.AvailableAt,
		&job.CreatedAt,
	); err != nil {
		return models.Job{}, err
	}

	return job, nil
}
