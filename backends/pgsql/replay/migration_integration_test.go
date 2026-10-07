//go:build integration

package replay_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/migrations"
	"github.com/assurrussa/outbox/backends/pgsql/migrator"
	"github.com/assurrussa/outbox/backends/pgsql/replay"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
)

func TestReplayDownWaitsForFirstReplayCommit(t *testing.T) {
	t.Run("guarded Down retains first committed replay", func(t *testing.T) {
		runReplayDownRace(t, true, func(ctx context.Context, db *sql.DB) error {
			return migrator.RunEmbedded(ctx, db, logger.Discard(), migrator.WithCommand("down"))
		})
	})
	t.Run("unguarded control loses first committed provenance", func(t *testing.T) {
		// Remove only the new lock from a test-owned migration copy. The same
		// coordinated interleaving must reproduce the pre-fix data loss.
		directory := unguardedMigrationCopy(t)
		runReplayDownRace(t, false, func(ctx context.Context, db *sql.DB) error {
			return migrator.Run(ctx, db, logger.Discard(),
				migrator.WithCommand("down"), migrator.WithDirectory(directory))
		})
	})
}

type replayOutcome struct {
	result replay.Result
	err    error
}

func runReplayDownRace(t *testing.T, guarded bool, down func(context.Context, *sql.DB) error) {
	t.Helper()
	f := openReplayFixture(t)
	downDB, application := openDownDB(t, f.pool)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	source := f.source(t, "order.created")
	request := requestFor(source)
	staged := make(chan int, 1)
	releaseCommit := make(chan struct{})
	var releaseOnce sync.Once
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(releaseCommit) })
		workers.Wait()
	})
	engine := commitGateEngine{DBEngine: f.client.engine, beforeCommit: func(ctx context.Context, tx pgx.Tx) error {
		var pid int
		if err := tx.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
			return err
		}
		staged <- pid
		select {
		case <-releaseCommit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r, err := replay.New(borrowedClient{engine: engine})
	require.NoError(t, err)
	replayed := make(chan replayOutcome, 1)
	workers.Go(func() {
		result, replayErr := r.Replay(ctx, request, admitOriginal)
		replayed <- replayOutcome{result: result, err: replayErr}
	})
	var replayPID int
	select {
	case replayPID = <-staged:
	case outcome := <-replayed:
		t.Fatalf("replay ended before commit gate: %+v", outcome)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var visible int
	require.NoError(t, f.pool.QueryRow(ctx, "select count(*) from outbox_job_replays").Scan(&visible))
	require.Zero(t, visible, "the first provenance row is still uncommitted")
	downResult := make(chan error, 1)
	workers.Go(func() { downResult <- down(ctx, downDB) })
	// Actual database lock observation, rather than elapsed time, determines
	// when the first replay may commit. Both old DROP and fixed LOCK block here.
	require.NoError(t, awaitDownLock(ctx, f.pool, application, replayPID))
	releaseOnce.Do(func() { close(releaseCommit) })
	outcome := <-replayed
	require.NoError(t, outcome.err)
	require.True(t, outcome.result.Created)
	downErr := <-downResult
	if guarded {
		require.ErrorContains(t, downErr, "replay provenance exists")
		assertMigrationState(t, f, 5, true)
		retained, lookupErr := f.replayer.ByRequestID(ctx, request.RequestID)
		require.NoError(t, lookupErr)
		require.Equal(t, outcome.result.Record, retained)
	} else {
		require.NoError(t, downErr)
		assertMigrationState(t, f, 4, false)
	}
	// The original failure and newly committed queue job survive in both
	// schedules; only the unguarded control destroys their replay provenance.
	job, err := f.jobs.GetByID(ctx, outcome.result.Record.JobID)
	require.NoError(t, err)
	require.Equal(t, source.Payload, job.Payload)
	var failures int
	require.NoError(t, f.pool.QueryRow(ctx, "select count(*) from jobs_failed where id=$1", source.FailedJobID).Scan(&failures))
	require.Equal(t, 1, failures)
}

func openDownDB(t *testing.T, pool *pgxpool.Pool) (*sql.DB, string) {
	t.Helper()
	config := pool.Config().Copy()
	application := "outbox_down_" + uuid.NewString()
	config.ConnConfig.RuntimeParams["application_name"] = application
	config.MaxConns = 1
	migrationPool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(migrationPool.Close)
	db := stdlib.OpenDBFromPool(migrationPool)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, application
}

func awaitDownLock(ctx context.Context, pool *pgxpool.Pool, application string, replayPID int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `select exists (
			select 1 from pg_locks l join pg_stat_activity a on a.pid=l.pid
			where a.application_name=$1 and a.xact_start is not null
			and l.relation='outbox_job_replays'::regclass
			and l.mode='AccessExclusiveLock' and not l.granted
			and $2=any(pg_blocking_pids(a.pid))
		)`, application, replayPID).Scan(&blocked)
		if err != nil {
			return fmt.Errorf("observe Down lock: %w", err)
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func unguardedMigrationCopy(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		data, readErr := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, readErr)
		if entry.Name() == "00005_add_job_replay_provenance.sql" {
			const lock = "lock table outbox_job_replays in access exclusive mode;\n"
			require.Contains(t, string(data), lock)
			data = []byte(strings.Replace(string(data), lock, "", 1))
		}
		require.NoError(t, os.WriteFile(filepath.Join(directory, entry.Name()), data, 0o600))
	}
	return directory
}

func TestReplayDownRollsBackDDLWithVersionMutation(t *testing.T) {
	f := openReplayFixture(t)
	// Failure after DROP but before migration version removal proves that
	// goose's transaction covers both DDL and version bookkeeping.
	_, err := f.pool.Exec(t.Context(), `create function reject_replay_down_version() returns trigger
		language plpgsql as $$ begin raise exception 'test-owned version deletion failure'; end $$;
		create trigger reject_replay_down_version before delete on goose_db_version
		for each row when (old.version_id=5) execute function reject_replay_down_version();`)
	require.NoError(t, err)
	err = migrator.RunEmbedded(t.Context(), f.sqlDB, logger.Discard(), migrator.WithCommand("down"))
	require.ErrorContains(t, err, "test-owned version deletion failure")
	assertMigrationState(t, f, 5, true)
	_, err = f.pool.Exec(t.Context(), `drop trigger reject_replay_down_version on goose_db_version;
		drop function reject_replay_down_version();`)
	require.NoError(t, err)
	// An empty journal still supports an intentional successful downgrade.
	require.NoError(t, migrator.RunEmbedded(t.Context(), f.sqlDB, logger.Discard(), migrator.WithCommand("down")))
	assertMigrationState(t, f, 4, false)
}

func assertMigrationState(t *testing.T, f replayFixture, wantVersion int64, wantTable bool) {
	t.Helper()
	version, err := goose.GetDBVersionContext(t.Context(), f.sqlDB)
	require.NoError(t, err)
	require.Equal(t, wantVersion, version)
	var tableExists bool
	require.NoError(t, f.pool.QueryRow(t.Context(), "select to_regclass('outbox_job_replays') is not null").Scan(&tableExists))
	require.Equal(t, wantTable, tableExists)
}

type commitGateEngine struct {
	storage.DBEngine
	beforeCommit func(context.Context, pgx.Tx) error
}

func (e commitGateEngine) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := e.DBEngine.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return commitGateTx{Tx: tx, beforeCommit: e.beforeCommit}, nil
}

type commitGateTx struct {
	pgx.Tx
	beforeCommit func(context.Context, pgx.Tx) error
}

func (t commitGateTx) Commit(ctx context.Context) error {
	if err := t.beforeCommit(ctx, t.Tx); err != nil {
		return err
	}
	return t.Tx.Commit(ctx)
}
