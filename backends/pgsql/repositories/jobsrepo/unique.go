package jobsrepo

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

func createUniqueJob(
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
	scan func(string, ...any) (types.JobID, error),
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

	storedJobID, err := scan(
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
		if pgxscan.NotFound(err) || errors.Is(err, sql.ErrNoRows) {
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
