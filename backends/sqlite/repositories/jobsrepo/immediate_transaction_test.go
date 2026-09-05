package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/migrator"
	"github.com/assurrussa/outbox/backends/sqlite/storage"
	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/types"
	_ "modernc.org/sqlite"
)

type interceptedImmediateConn struct {
	*sql.Conn
	exec func(context.Context, string, ...any) (sql.Result, error)
}

func (c interceptedImmediateConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.exec(ctx, query, args...)
}

func TestImmediateTransactionCleansOrDiscardsConnection(t *testing.T) {
	workErr := errors.New("work failed")
	commitErr := errors.New("commit failed")
	rollbackErr := errors.New("rollback failed")
	for _, test := range []struct {
		name       string
		cancelAt   string
		failCommit bool
		failClean  bool
		beginError bool
		wantErr    error
		discarded  bool
	}{
		{name: "cancel after begin", cancelAt: "BEGIN IMMEDIATE;", wantErr: context.Canceled},
		{name: "cancel before commit", cancelAt: "work", wantErr: context.Canceled},
		{name: "work error", wantErr: workErr},
		{name: "commit error", failCommit: true, wantErr: commitErr},
		{name: "rollback error", failClean: true, wantErr: workErr, discarded: true},
		{name: "ambiguous begin", beginError: true, wantErr: context.Canceled, discarded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cleanup.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			_, err = db.ExecContext(t.Context(), "CREATE TABLE effects(value TEXT); CREATE TEMP TABLE session_marker(value TEXT);")
			require.NoError(t, err)
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rollbackCalled := false
			wrapped := interceptedImmediateConn{Conn: conn}
			wrapped.exec = func(callCtx context.Context, query string, args ...any) (sql.Result, error) {
				if query == "ROLLBACK;" {
					rollbackCalled = true
					require.NoError(t, callCtx.Err(), "cleanup must outlive work cancellation")
					deadline, ok := callCtx.Deadline()
					require.True(t, ok)
					require.InDelta(t, immediateRollbackTimeout.Seconds(), time.Until(deadline).Seconds(), 1)
					if test.failClean {
						return nil, rollbackErr
					}
				}
				if query == "COMMIT;" && test.failCommit {
					return nil, commitErr
				}
				result, execErr := conn.ExecContext(callCtx, query, args...)
				if execErr == nil && query == test.cancelAt {
					cancel()
				}
				if execErr == nil && query == "BEGIN IMMEDIATE;" && test.beginError {
					return nil, context.Canceled
				}
				return result, execErr
			}
			err = runImmediateTransaction(ctx, wrapped, func(exec sqlExecutor) error {
				if _, err := exec.ExecContext(ctx, "INSERT INTO effects VALUES ('aborted');"); err != nil {
					return err
				}
				if test.cancelAt == "work" {
					cancel()
					return nil
				}
				if test.failCommit {
					return nil
				}
				return workErr
			})
			require.ErrorIs(t, err, test.wantErr)
			if test.failClean {
				require.ErrorIs(t, err, rollbackErr)
			}
			require.Equal(t, !test.beginError, rollbackCalled)
			var count int
			err = db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM session_marker;").Scan(&count)
			if test.discarded {
				require.ErrorContains(t, err, "no such table", "an uncertain connection must not be reused")
			} else {
				require.NoError(t, err, "the same physical connection must remain reusable")
			}
			conn, err = db.Conn(t.Context())
			require.NoError(t, err)
			require.NoError(t, runImmediateTransaction(t.Context(), conn, func(exec sqlExecutor) error {
				_, err := exec.ExecContext(t.Context(), "INSERT INTO effects VALUES ('next');")
				return err
			}))
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM effects;").Scan(&count))
			require.Equal(t, 1, count, "failed work must not survive rollback or connection discard")
		})
	}
}

func newImmediateTransactionRepo(t *testing.T) *Repo {
	t.Helper()
	client, err := storage.Create(t.Context(), filepath.Join(t.TempDir(), "jobs.db"),
		storage.WithMaxOpenConns(1), storage.WithMaxIdleConns(1))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, migrator.RunEmbedded(t.Context(), client.DB(), logger.Discard(), migrator.WithCommand("up")))
	return Must(client)
}

func TestImmediateTransactionCancellationProtectsClaimsAndUniquePut(t *testing.T) {
	for _, path := range []string{"ordinary", "bounded", "unique"} {
		t.Run(path, func(t *testing.T) {
			r := newImmediateTransactionRepo(t)
			db := r.client.DB()
			now := time.Now().UTC().Truncate(time.Millisecond)
			capability := coreoutbox.JobCapability{Name: "publish", SchemaVersion: 1}
			var seededID types.JobID
			if path != "unique" {
				var err error
				seededID, err = r.CreateJobVersioned(t.Context(), capability.Name, capability.SchemaVersion, `{}`, now)
				require.NoError(t, err)
			}
			_, err := db.ExecContext(t.Context(), "CREATE TEMP TABLE session_marker(value TEXT);")
			require.NoError(t, err)
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapped := interceptedImmediateConn{Conn: conn}
			wrapped.exec = func(callCtx context.Context, query string, args ...any) (sql.Result, error) {
				result, err := conn.ExecContext(callCtx, query, args...)
				if err == nil && ((path != "unique" && query == "BEGIN IMMEDIATE;") ||
					(path == "unique" && strings.HasPrefix(query, "INSERT INTO ") && strings.Contains(query, defaultIdempotencyTableName))) {
					cancel()
				}
				return result, err
			}
			err = runImmediateTransaction(ctx, wrapped, func(exec sqlExecutor) error {
				token := types.NewLeaseToken()
				switch path {
				case "ordinary":
					_, err := r.findAndReserveBatchWithExecutor(ctx, exec, now, now.Add(time.Minute), token,
						[]coreoutbox.JobCapability{capability}, 1)
					return err
				case "bounded":
					_, err := r.findAndReserveBatchBoundedWithExecutor(ctx, exec, now, now.Add(time.Minute), token,
						capability, coreoutbox.BatchClaimLimits{MaxMessages: 1, MaxBytes: 1024})
					return err
				default:
					_, err := r.createJobVersionedUnique(ctx, exec, "event", capability.Name, capability.SchemaVersion, `{}`, now)
					return err
				}
			})
			require.ErrorIs(t, err, context.Canceled)
			var count int
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM session_marker;").Scan(&count))
			if path == "unique" {
				require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM outbox_job_idempotency_keys;").Scan(&count))
				require.Zero(t, count, "cancelled unique put must not leak its idempotency registration")
				require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM jobs;").Scan(&count))
				require.Zero(t, count)
				created, err := r.CreateJobVersionedUniqueResult(t.Context(), "event", capability.Name, capability.SchemaVersion, `{}`, now)
				require.NoError(t, err)
				require.True(t, created.Created)
				return
			}
			stored, err := r.GetByID(t.Context(), seededID)
			require.NoError(t, err)
			require.Zero(t, stored.Attempts)
			require.False(t, stored.ReservedAt.Valid)
			if path == "ordinary" {
				jobs, err := r.FindAndReserveJobsForCapabilities(t.Context(), now, now.Add(time.Minute), types.NewLeaseToken(),
					[]coreoutbox.JobCapability{capability}, 1)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
			} else {
				jobs, err := r.FindAndReserveJobsForCapabilityBounded(t.Context(), now, now.Add(time.Minute), types.NewLeaseToken(),
					capability, coreoutbox.BatchClaimLimits{MaxMessages: 1, MaxBytes: 1024})
				require.NoError(t, err)
				require.Len(t, jobs, 1)
			}
		})
	}
}

func TestImmediateTransactionPreservesCallerTransactionOwnership(t *testing.T) {
	r := newImmediateTransactionRepo(t)
	rollbackErr := errors.New("caller rolls back")
	now := time.Now().UTC()
	err := transaction.New(r.client.DB()).RunInTx(t.Context(), func(ctx context.Context) error {
		_, err := r.CreateJobVersionedUniqueResult(ctx, "event", "publish", 1, `{}`, now)
		require.NoError(t, err)
		jobs, err := r.FindAndReserveJobsForCapabilities(ctx, now, now.Add(time.Minute), types.NewLeaseToken(),
			[]coreoutbox.JobCapability{{Name: "publish", SchemaVersion: 1}}, 1)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)
	var count int
	require.NoError(t, r.client.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM jobs;").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, r.client.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM outbox_job_idempotency_keys;").Scan(&count))
	require.Zero(t, count)
}
