//go:build integration

package jobsrepo_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/migrator"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
)

const sqlTxTestSQLName = "sql.event"

func TestSQLTxPutterAtomicCommitAndRollback(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			f := openSQLTxFixture(t)
			tx, putter := f.begin(t)
			_, err := tx.ExecContext(t.Context(), "insert into business_effects (id) values (1)")
			require.NoError(t, err)
			at := time.Now().UTC().Truncate(time.Microsecond)
			result, err := putter.PutVersionedUnique(t.Context(), "atomic", "business.changed", 2, "original", at)
			require.NoError(t, err)
			require.True(t, result.Created)

			var inside int
			require.NoError(t, tx.QueryRowContext(t.Context(), "select count(*) from jobs").Scan(&inside))
			require.Equal(t, 1, inside)
			// Independent observers cannot see either write before the owner commits.
			f.counts(t, 0, 0, 0)
			if commit {
				require.NoError(t, tx.Commit())
				f.counts(t, 1, 1, 1)
				job, err := f.jobs.GetByID(t.Context(), result.JobID)
				require.NoError(t, err)
				require.Equal(t, "business.changed", job.Name)
				require.Equal(t, coreoutbox.SchemaVersion(2), job.SchemaVersion)
				require.Equal(t, "original", job.Payload)
				require.True(t, at.Equal(job.AvailableAt))
				require.Zero(t, job.Attempts)
				require.False(t, job.ReservedAt.Valid)
				require.True(t, job.LeaseToken.IsZero())
			} else {
				require.NoError(t, tx.Rollback())
				f.counts(t, 0, 0, 0)
				retry, err := f.jobs.CreateJobVersionedUniqueResult(
					t.Context(), "atomic", "business.changed", 2, "original", at,
				)
				require.NoError(t, err)
				require.True(t, retry.Created)
				require.NotEqual(t, result.JobID, retry.JobID)
			}
			ended, err := putter.PutVersionedUnique(t.Context(), "ended", "business.changed", 2, "original", at)
			require.ErrorIs(t, err, sql.ErrTxDone)
			require.Zero(t, ended)
		})
	}
}

func TestSQLTxPutterUniqueParityAndRetainedIdentity(t *testing.T) {
	f := openSQLTxFixture(t)
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	pgxResult, err := f.jobs.CreateJobVersionedUniqueResult(t.Context(), "pgx-first", "pgx.event", 1, sqlTxTestPayload, at)
	require.NoError(t, err)

	tx, putter := f.begin(t)
	replayed, err := putter.PutVersionedUnique(
		t.Context(), "pgx-first", "pgx.event", 1, sqlTxTestPayload, at.In(time.FixedZone("same-instant", 7200)),
	)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, pgxResult.JobID, replayed.JobID)
	first, err := putter.PutVersionedUnique(t.Context(), "sql-first", sqlTxTestSQLName, 2, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.True(t, first.Created)
	repeated, err := putter.PutVersionedUnique(t.Context(), "sql-first", sqlTxTestSQLName, 2, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.False(t, repeated.Created)
	require.Equal(t, first.JobID, repeated.JobID)

	for _, changed := range []struct {
		name    string
		version coreoutbox.SchemaVersion
		payload string
		at      time.Time
	}{
		{name: "other.event", version: 2, payload: sqlTxTestPayload, at: at},
		{name: sqlTxTestSQLName, version: 3, payload: sqlTxTestPayload, at: at},
		{name: sqlTxTestSQLName, version: 2, payload: "different", at: at},
		{name: sqlTxTestSQLName, version: 2, payload: sqlTxTestPayload, at: at.Add(time.Microsecond)},
	} {
		conflict, err := putter.PutVersionedUnique(
			t.Context(), "sql-first", changed.name, changed.version, changed.payload, changed.at,
		)
		require.ErrorIs(t, err, coreoutbox.ErrIdempotencyConflict)
		require.Zero(t, conflict)
	}
	// A logical identity conflict does not finalize or abort the owner's transaction.
	require.NoError(t, tx.Commit())
	f.counts(t, 0, 2, 2)
	pgxReplay, err := f.jobs.CreateJobVersionedUniqueResult(t.Context(), "sql-first", sqlTxTestSQLName, 2, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.False(t, pgxReplay.Created)
	require.Equal(t, first.JobID, pgxReplay.JobID)

	token := types.NewLeaseToken()
	now := time.Now().UTC()
	claimed, err := f.jobs.FindAndReserveJobsForCapabilities(
		t.Context(), now, now.Add(time.Minute), token,
		[]coreoutbox.JobCapability{{Name: sqlTxTestSQLName, SchemaVersion: 2}}, 1,
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, first.JobID, claimed[0].ID)
	affected, err := f.jobs.DeleteJobWithLease(t.Context(), first.JobID, token, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	tx, putter = f.begin(t)
	afterACK, err := putter.PutVersionedUnique(t.Context(), "sql-first", sqlTxTestSQLName, 2, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.False(t, afterACK.Created)
	require.Equal(t, first.JobID, afterACK.JobID)
	require.NoError(t, tx.Commit())
	f.counts(t, 0, 1, 2)
}

func TestSQLTxPutterEmptyNameParity(t *testing.T) {
	f := openSQLTxFixture(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	pgxResult, err := f.jobs.CreateJobVersionedUniqueResult(t.Context(), "pgx-empty", "", 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.True(t, pgxResult.Created)

	tx, putter := f.begin(t)
	replayed, err := putter.PutVersionedUnique(t.Context(), "pgx-empty", "", 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, pgxResult.JobID, replayed.JobID)
	sqlResult, err := putter.PutVersionedUnique(t.Context(), "sql-empty", "", 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.True(t, sqlResult.Created)
	require.NoError(t, tx.Commit())

	pgxReplay, err := f.jobs.CreateJobVersionedUniqueResult(t.Context(), "sql-empty", "", 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.False(t, pgxReplay.Created)
	require.Equal(t, sqlResult.JobID, pgxReplay.JobID)
	job, err := f.jobs.GetByID(t.Context(), sqlResult.JobID)
	require.NoError(t, err)
	require.Empty(t, job.Name)
	f.counts(t, 0, 2, 2)
}

func TestSQLTxPutterWrongSchemaFailsWithoutFallback(t *testing.T) {
	f := openSQLTxFixture(t)
	tx, putter := f.begin(t)
	_, err := tx.ExecContext(t.Context(), "set local search_path to pg_catalog")
	require.NoError(t, err)
	result, err := putter.PutVersionedUnique(t.Context(), "wrong-schema", sqlTxTestEventName, 1, sqlTxTestPayload, time.Now())
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42P01", pgErr.Code)
	require.Zero(t, result)
	require.NoError(t, tx.Rollback())
	f.counts(t, 0, 0, 0)
}

func TestSQLTxPutterConstraintFailureRollsBackWithBusinessWrite(t *testing.T) {
	f := openSQLTxFixture(t)
	_, err := f.pool.Exec(t.Context(), "alter table jobs add constraint reject_payload check (payload <> 'reject')")
	require.NoError(t, err)
	tx, putter := f.begin(t)
	_, err = tx.ExecContext(t.Context(), "insert into business_effects (id) values (1)")
	require.NoError(t, err)
	result, err := putter.PutVersionedUnique(t.Context(), "rejected", sqlTxTestEventName, 1, "reject", time.Now())
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
	require.Zero(t, result)
	// Only the caller ends the aborted transaction.
	require.NoError(t, tx.Rollback())
	f.counts(t, 0, 0, 0)
}

func TestSQLTxPutterCallerControlsSavepoints(t *testing.T) {
	f := openSQLTxFixture(t)
	tx, putter := f.begin(t)
	_, err := tx.ExecContext(t.Context(), "insert into business_effects (id) values (1)")
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), "savepoint business_attempt")
	require.NoError(t, err)
	at := time.Now().UTC()
	discarded, err := putter.PutVersionedUnique(t.Context(), "discarded", sqlTxTestEventName, 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.True(t, discarded.Created)
	_, err = tx.ExecContext(t.Context(), "rollback to savepoint business_attempt")
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), "release savepoint business_attempt")
	require.NoError(t, err)
	retained, err := putter.PutVersionedUnique(t.Context(), "retained", sqlTxTestEventName, 1, sqlTxTestPayload, at)
	require.NoError(t, err)
	require.True(t, retained.Created)
	require.NoError(t, tx.Commit())
	f.counts(t, 1, 1, 1)
	var discardedKeys int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		"select count(*) from outbox_job_idempotency_keys where deduplication_key = 'discarded'",
	).Scan(&discardedKeys))
	require.Zero(t, discardedKeys)
}

type sqlTxFixture struct {
	sqlDB *sql.DB
	pool  *pgxpool.Pool
	jobs  *jobsrepo.Repo
}

func openSQLTxFixture(t *testing.T) sqlTxFixture {
	t.Helper()
	dsn := os.Getenv("OUTBOX_PG_DSN")
	if dsn == "" {
		t.Skip("set OUTBOX_PG_DSN to an owned disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := "outbox_sqltx_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "create schema "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, dropErr := admin.Exec(cleanupCtx, "drop schema "+quoted+" cascade")
		require.NoError(t, dropErr)
	})
	config, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	db := stdlib.OpenDB(*config.ConnConfig)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, migrator.RunEmbedded(ctx, db, logger.Discard(), migrator.WithCommand("up")))
	_, err = pool.Exec(ctx, "create table business_effects (id integer primary key)")
	require.NoError(t, err)
	client := sqlTxFixtureClient{engine: pgsqlclient.NewDBEngine(pool, "test", logger.Discard())}
	jobs, err := jobsrepo.New(jobsrepo.NewOptions(client))
	require.NoError(t, err)
	return sqlTxFixture{sqlDB: db, pool: pool, jobs: jobs}
}

func (f sqlTxFixture) begin(t *testing.T) (*sql.Tx, *jobsrepo.SQLTxPutter) {
	t.Helper()
	tx, err := f.sqlDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	putter, err := jobsrepo.NewSQLTxPutter(tx)
	require.NoError(t, err)
	return tx, putter
}

func (f sqlTxFixture) counts(t *testing.T, business, jobs, keys int) {
	t.Helper()
	var actualBusiness, actualJobs, actualKeys int
	err := f.pool.QueryRow(t.Context(), `
		select (select count(*) from business_effects),
			(select count(*) from jobs),
			(select count(*) from outbox_job_idempotency_keys)`,
	).Scan(&actualBusiness, &actualJobs, &actualKeys)
	require.NoError(t, err)
	require.Equal(t, business, actualBusiness, "business rows")
	require.Equal(t, jobs, actualJobs, "queued jobs")
	require.Equal(t, keys, actualKeys, "idempotency keys")
}

type sqlTxFixtureClient struct{ engine storage.DBEngine }

func (c sqlTxFixtureClient) DB() storage.DBEngine { return c.engine }
func (sqlTxFixtureClient) Close() error           { return nil }
