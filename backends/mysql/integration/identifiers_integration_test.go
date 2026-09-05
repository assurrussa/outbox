//go:build integration

package outbox_test

import (
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql/migrations"
	mysqltests "github.com/assurrussa/outbox/backends/mysql/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

func TestMySQLUniqueKeysUseExactBytes(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)
	now := time.Now().UTC().Truncate(time.Microsecond)
	keys := []string{"EventA", "eventa", "eventa "}
	ids := make(map[string]types.JobID, len(keys))
	for _, key := range keys {
		result, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, key, "publish", 1, `{}`, now)
		require.NoError(t, err)
		require.True(t, result.Created, "independent key %q must create a job", key)
		for _, previous := range ids {
			require.NotEqual(t, previous, result.JobID)
		}
		ids[key] = result.JobID
	}
	for _, key := range keys {
		result, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, key, "publish", 1, `{}`, now)
		require.NoError(t, err)
		require.False(t, result.Created)
		require.Equal(t, ids[key], result.JobID)
	}
	_, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, keys[0], "publish", 1, `{"different":true}`, now)
	require.ErrorIs(t, err, outbox.ErrIdempotencyConflict)

	items := []outbox.UniqueBatchPut{
		{DeduplicationKey: "BatchA", Name: "publish", SchemaVersion: 1, Payload: `{}`, AvailableAt: now},
		{DeduplicationKey: "batcha", Name: "publish", SchemaVersion: 1, Payload: `{}`, AvailableAt: now},
		{DeduplicationKey: "batcha ", Name: "publish", SchemaVersion: 1, Payload: `{}`, AvailableAt: now},
	}
	created, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, items)
	require.NoError(t, err)
	require.Len(t, created, len(items))
	for i, result := range created {
		require.True(t, result.Created)
		for j := 0; j < i; j++ {
			require.NotEqual(t, created[j].JobID, result.JobID)
		}
	}
	replayed, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, items)
	require.NoError(t, err)
	for i, result := range replayed {
		require.False(t, result.Created)
		require.Equal(t, created[i].JobID, result.JobID)
	}

	token := types.NewLeaseToken()
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(ctx, now, now.Add(time.Minute), token,
		[]outbox.JobCapability{{Name: "publish", SchemaVersion: 1}}, 10)
	require.NoError(t, err)
	affected, err := ts.jobsRepo.DeleteJobWithLease(ctx, ids[keys[0]], token, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
	tombstone, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, keys[0], "publish", 1, `{}`, now)
	require.NoError(t, err)
	require.False(t, tombstone.Created)
	require.Equal(t, ids[keys[0]], tombstone.JobID)
}

func TestMySQLCapabilityNamesAreExact(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		name := "ordinary"
		if bounded {
			name = "bounded"
		}
		t.Run(name, func(t *testing.T) {
			ctx, _, ts := NewTestMySQLSuite(t)
			defer ts.cleanUp(ctx)
			now := time.Now().UTC().Truncate(time.Microsecond)
			names := []string{"send_email", "Send_Email", "send_email ", "café", "cafe\u0301", "cafe"}
			ids := make(map[string]types.JobID, len(names))
			for _, name := range names {
				ids[name] = createMySQLBatchJob(t, ctx, ts, name, 1, now)
			}
			stats, err := ts.jobsRepo.GetQueueStats(ctx, now)
			require.NoError(t, err)
			require.Len(t, stats.ByCapability, len(names))
			for _, exact := range []string{"send_email", "café"} {
				capability := outbox.JobCapability{Name: exact, SchemaVersion: 1}
				token := types.NewLeaseToken()
				if bounded {
					jobs, err := ts.jobsRepo.FindAndReserveJobsForCapabilityBounded(ctx, now, now.Add(time.Minute), token,
						capability, outbox.BatchClaimLimits{MaxMessages: 10, MaxBytes: 1024})
					require.NoError(t, err)
					require.Equal(t, []types.JobID{ids[exact]}, mysqlBatchJobIDs(jobs))
				} else {
					jobs, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(ctx, now, now.Add(time.Minute), token,
						[]outbox.JobCapability{capability}, 10)
					require.NoError(t, err)
					require.Equal(t, []types.JobID{ids[exact]}, mysqlBatchJobIDs(jobs))
				}
			}
			for _, unsupported := range []string{"Send_Email", "send_email ", "cafe\u0301", "cafe"} {
				job, err := ts.jobsRepo.GetByID(ctx, ids[unsupported])
				require.NoError(t, err)
				require.Zero(t, job.Attempts)
				require.False(t, job.ReservedAt.Valid)
				require.True(t, job.LeaseToken.IsZero())
			}
		})
	}
}

func TestMySQLExactIdentifierMigrationPreservesVersionFiveData(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t, mysqltests.WithDatabasePathFilesMigration())
	defer ts.cleanUp(ctx)
	provider, err := goose.NewProvider(goose.DialectMySQL, ts.db.DB(), migrations.FS, goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 5)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, "EventA", "Café ", 2, `{"preserved":true}`, now)
	require.NoError(t, err)
	before, err := ts.jobsRepo.GetByID(ctx, first.JobID)
	require.NoError(t, err)
	failedID, err := ts.jobsFailedRepo.CreateFailedJobVersioned(ctx, types.NewJobID(), "Café ", 2, `{}`, "preserved")
	require.NoError(t, err)
	failedBefore, err := ts.jobsFailedRepo.GetByID(ctx, failedID)
	require.NoError(t, err)
	var fingerprint string
	require.NoError(t, ts.db.DB().QueryRowContext(ctx,
		"SELECT fingerprint FROM outbox_job_idempotency_keys WHERE deduplication_key = ?", "EventA").Scan(&fingerprint))

	_, err = provider.UpTo(ctx, 6)
	require.NoError(t, err)
	after, err := ts.jobsRepo.GetByID(ctx, first.JobID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	failedAfter, err := ts.jobsFailedRepo.GetByID(ctx, failedID)
	require.NoError(t, err)
	require.Equal(t, failedBefore, failedAfter)
	var fingerprintAfter string
	require.NoError(t, ts.db.DB().QueryRowContext(ctx,
		"SELECT fingerprint FROM outbox_job_idempotency_keys WHERE deduplication_key = ?", "EventA").Scan(&fingerprintAfter))
	require.Equal(t, fingerprint, fingerprintAfter)
	second, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, "eventa", "Café ", 2, `{"preserved":true}`, now)
	require.NoError(t, err)
	require.True(t, second.Created)
	require.NotEqual(t, first.JobID, second.JobID)

	_, err = provider.Down(ctx)
	require.ErrorContains(t, err, "cannot be reversed")
	version, err := provider.GetDBVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(6), version)
	for _, key := range []string{"EventA", "eventa"} {
		result, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, key, "Café ", 2, `{"preserved":true}`, now)
		require.NoError(t, err)
		require.False(t, result.Created)
	}
	for _, table := range []string{"jobs", "jobs_failed"} {
		var collation string
		require.NoError(t, ts.db.DB().QueryRowContext(ctx, `SELECT COLLATION_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'name'`, table).Scan(&collation))
		require.Equal(t, "utf8mb4_0900_bin", collation)
	}
}

func TestMySQLMigrationHarmonizesDivergentBatchReplayKeys(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t, mysqltests.WithDatabasePathFilesMigration())
	defer ts.cleanUp(ctx)
	provider, err := goose.NewProvider(goose.DialectMySQL, ts.db.DB(), migrations.FS, goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 5)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	// 1. Create initial job with key "EventA" under version 5
	first, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, "EventA", "publish", 1, `{"v":1}`, now)
	require.NoError(t, err)
	require.True(t, first.Created)

	// 2. Batch replay with key "eventa" (case variation) under version 5
	replayed, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, []outbox.UniqueBatchPut{
		{DeduplicationKey: "eventa", Name: "publish", SchemaVersion: 1, Payload: `{"v":1}`, AvailableAt: now},
	})
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	require.False(t, replayed[0].Created)
	require.Equal(t, first.JobID, replayed[0].JobID)

	// Verify that due to old ON DUPLICATE KEY UPDATE in version 5, the registry key diverged from active job key
	var activeKeyHex, registryKeyHex string
	err = ts.db.DB().QueryRowContext(ctx, `
		SELECT HEX(j.deduplication_key), HEX(k.deduplication_key)
		FROM jobs j
		JOIN outbox_job_idempotency_keys k ON k.job_id = j.id
		WHERE j.id = ?
	`, first.JobID.String()).Scan(&activeKeyHex, &registryKeyHex)
	require.NoError(t, err)
	require.NotEqual(t, activeKeyHex, registryKeyHex, "pre-migration keys must diverge after batch replay")

	// 3. Migrate to version 6 (enforcing exact identifiers)
	_, err = provider.UpTo(ctx, 6)
	require.NoError(t, err)

	// Verify that migration harmonized the keys
	err = ts.db.DB().QueryRowContext(ctx, `
		SELECT HEX(j.deduplication_key), HEX(k.deduplication_key)
		FROM jobs j
		JOIN outbox_job_idempotency_keys k ON k.job_id = j.id
		WHERE j.id = ?
	`, first.JobID.String()).Scan(&activeKeyHex, &registryKeyHex)
	require.NoError(t, err)
	require.Equal(t, activeKeyHex, registryKeyHex, "migration must harmonize divergent keys")

	// 4. Complete the job (ACK)
	token := types.NewLeaseToken()
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(ctx, now, now.Add(time.Minute), token,
		[]outbox.JobCapability{{Name: "publish", SchemaVersion: 1}}, 10)
	require.NoError(t, err)
	affected, err := ts.jobsRepo.DeleteJobWithLease(ctx, first.JobID, token, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	// 5. Re-putting "EventA" must return the tombstoned original job ID with Created == false
	tombstone, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, "EventA", "publish", 1, `{"v":1}`, now)
	require.NoError(t, err)
	require.False(t, tombstone.Created, "must detect existing idempotency key")
	require.Equal(t, first.JobID, tombstone.JobID)
}
