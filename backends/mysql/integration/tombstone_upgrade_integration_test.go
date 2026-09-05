//go:build integration

package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql/migrations"
	mysqltests "github.com/assurrussa/outbox/backends/mysql/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

type legacyMySQLKey struct {
	ctx         context.Context
	suite       *TestMySQLSuite
	migrations  *goose.Provider
	jobID       types.JobID
	availableAt time.Time
	fingerprint string
	createdAt   time.Time
}

func newLegacyMySQLKey(t *testing.T, completed bool) legacyMySQLKey {
	t.Helper()
	ctx, _, ts := NewTestMySQLSuite(t, mysqltests.WithDatabasePathFilesMigration())
	t.Cleanup(func() { ts.cleanUp(ctx) })
	provider, err := goose.NewProvider(goose.DialectMySQL, ts.db.DB(), migrations.FS, goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 5)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := ts.jobsRepo.CreateJobVersionedUniqueResult(ctx, "EventA", "publish", 1, `{"v":1}`, now)
	require.NoError(t, err)
	replayed, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, []outbox.UniqueBatchPut{
		{DeduplicationKey: "eventa", Name: "publish", SchemaVersion: 1, Payload: `{"v":1}`, AvailableAt: now},
	})
	require.NoError(t, err)
	require.Equal(t, []outbox.UniquePutResult{{JobID: first.JobID, Created: false}}, replayed)
	if completed {
		token := types.NewLeaseToken()
		_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(ctx, now, now.Add(time.Minute), token,
			[]outbox.JobCapability{{Name: "publish", SchemaVersion: 1}}, 1)
		require.NoError(t, err)
		affected, deleteErr := ts.jobsRepo.DeleteJobWithLease(ctx, first.JobID, token, now)
		require.NoError(t, deleteErr)
		require.Equal(t, int64(1), affected)
	}
	fixture := legacyMySQLKey{ctx: ctx, suite: ts, migrations: provider, jobID: first.JobID, availableAt: now}
	require.NoError(t, ts.db.DB().QueryRowContext(ctx,
		"SELECT fingerprint, created_at FROM outbox_job_idempotency_keys WHERE job_id = ?", first.JobID,
	).Scan(&fixture.fingerprint, &fixture.createdAt))
	return fixture
}

func TestMySQLCompletedKeyMigrationNeedsOriginalHistory(t *testing.T) {
	f := newLegacyMySQLKey(t, true)
	_, err := f.migrations.UpTo(f.ctx, 6)
	require.NoError(t, err)
	retained, err := f.suite.jobsRepo.CreateJobVersionedUniqueResult(f.ctx, "eventa", "publish", 1, `{"v":1}`, f.availableAt)
	require.NoError(t, err)
	require.Equal(t, outbox.UniquePutResult{JobID: f.jobID, Created: false}, retained)
	// This is the documented information-loss boundary, not automatic recovery:
	// no active row or fingerprint can reconstruct the original key spelling.
	original, err := f.suite.jobsRepo.CreateJobVersionedUniqueResult(f.ctx, "EventA", "publish", 1, `{"v":1}`, f.availableAt)
	require.NoError(t, err)
	require.True(t, original.Created)
	require.NotEqual(t, f.jobID, original.JobID)
}

func beginMySQLKeyRepair(t *testing.T, f legacyMySQLKey) (*sql.Tx, string) {
	t.Helper()
	statement, err := os.ReadFile("../docs/restore-tombstone-key.sql")
	require.NoError(t, err)
	tx, err := f.suite.db.DB().BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Errorf("rollback repair transaction: %v", rollbackErr)
		}
	})
	return tx, string(statement)
}

func TestMySQLCompletedKeyRecoveryBeforeMigration(t *testing.T) {
	f := newLegacyMySQLKey(t, true)
	tx, statement := beginMySQLKeyRepair(t, f)
	result, err := tx.ExecContext(f.ctx, statement, "EventA", f.jobID, f.fingerprint, "eventa", "EventA")
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
	var key, fingerprint string
	var jobID types.JobID
	var createdAt time.Time
	require.NoError(t, tx.QueryRowContext(f.ctx,
		"SELECT deduplication_key, job_id, fingerprint, created_at FROM outbox_job_idempotency_keys WHERE job_id = ?", f.jobID,
	).Scan(&key, &jobID, &fingerprint, &createdAt))
	require.Equal(t, "EventA", key)
	require.Equal(t, f.jobID, jobID)
	require.Equal(t, f.fingerprint, fingerprint)
	require.Equal(t, f.createdAt, createdAt)
	require.NoError(t, tx.Commit())
	_, err = f.migrations.UpTo(f.ctx, 6)
	require.NoError(t, err)
	replayed, err := f.suite.jobsRepo.CreateJobVersionedUniqueResult(f.ctx, "EventA", "publish", 1, `{"v":1}`, f.availableAt)
	require.NoError(t, err)
	require.Equal(t, outbox.UniquePutResult{JobID: f.jobID, Created: false}, replayed)
}

func TestMySQLCompletedKeyRecoveryGuards(t *testing.T) {
	for _, scenario := range []string{"fingerprint", "key-case", "key-space", "job-id", "active", "unchanged", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLegacyMySQLKey(t, scenario != "active")
			key, observed, fingerprint, jobID := "EventA", "eventa", f.fingerprint, f.jobID
			switch scenario {
			case "fingerprint":
				fingerprint = strings.ToUpper(fingerprint)
				require.NotEqual(t, f.fingerprint, fingerprint)
			case "key-case":
				observed = "EventA"
			case "key-space":
				observed = "eventa "
			case "job-id":
				jobID = types.NewJobID()
			case "unchanged":
				key = observed
			case "conflict":
				key = "occupied"
				_, err := f.suite.jobsRepo.CreateJobVersionedUniqueResult(f.ctx, key, "publish", 1, `{}`, f.availableAt)
				require.NoError(t, err)
			}
			tx, statement := beginMySQLKeyRepair(t, f)
			result, err := tx.ExecContext(f.ctx, statement, key, jobID, fingerprint, observed, key)
			if scenario == "conflict" {
				require.ErrorContains(t, err, "Duplicate entry")
			} else {
				require.NoError(t, err)
				affected, rowsErr := result.RowsAffected()
				require.NoError(t, rowsErr)
				require.Zero(t, affected)
			}
			require.NoError(t, tx.Rollback())
			var retained, storedFingerprint string
			var createdAt time.Time
			require.NoError(t, f.suite.db.DB().QueryRowContext(f.ctx,
				"SELECT deduplication_key, fingerprint, created_at FROM outbox_job_idempotency_keys WHERE job_id = ?", f.jobID,
			).Scan(&retained, &storedFingerprint, &createdAt))
			require.Equal(t, "eventa", retained)
			require.Equal(t, f.fingerprint, storedFingerprint)
			require.Equal(t, f.createdAt, createdAt)
		})
	}
}
