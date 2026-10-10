//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	sharedtests "github.com/assurrussa/outbox/shared/tests"
)

// Only the unique database created by NewTestRepoSuite becomes unavailable.
// This is a real connection outage, not a server-process or power-loss test.
func TestPostgresConnectionOutageBeforeAckRecoversBacklog(t *testing.T) {
	suiteCtx, _, ts := NewTestRepoSuite(t)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ts.cleanUp(cleanupCtx)
	}()
	ctx, cancel := context.WithTimeout(suiteCtx, 30*time.Second)
	defer cancel()
	pool := ts.db.DB().Pool()
	database := pool.Config().ConnConfig.Database
	controlConfig := pool.Config().ConnConfig.Copy()
	controlConfig.Database = sharedtests.Config.PostgresDatabase
	require.NotEqual(t, controlConfig.Database, database)
	require.Contains(t, database, "TestJobsSuite")
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	require.NoError(t, err)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, control.Close(cleanupCtx))
	}()
	var ownedAndAvailable bool
	err = control.QueryRow(ctx, `SELECT datallowconn AND datdba =
		(SELECT usesysid FROM pg_user WHERE usename = current_user)
		FROM pg_database WHERE datname = $1`, database).Scan(&ownedAndAvailable)
	require.NoError(t, err)
	require.True(t, ownedAndAvailable, "fault target must be an owned available fixture")
	allowSQL := "ALTER DATABASE " + pgx.Identifier{database}.Sanitize() + " ALLOW_CONNECTIONS true"
	// A fresh control connection restores availability even after a canceled
	// fault query has made the original control connection unusable.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupControl, openErr := pgx.ConnectConfig(cleanupCtx, controlConfig)
		if openErr != nil {
			t.Errorf("open fixture availability cleanup connection: %v", openErr)
			return
		}
		defer func() { require.NoError(t, cleanupControl.Close(cleanupCtx)) }()
		_, restoreErr := cleanupControl.Exec(cleanupCtx, allowSQL)
		require.NoError(t, restoreErr, "restore only the owned fixture before its normal cleanup")
	}()

	const jobName = "postgres-connection-outage"
	const jobCount = 8
	expected := make(map[outbox.JobID]string, jobCount)
	attempts := make(map[outbox.JobID]int, jobCount)
	firstStarted := make(map[outbox.JobID]time.Time, jobCount)
	lastStarted := make(map[outbox.JobID]time.Time, jobCount)
	acked := make(chan outbox.JobID, jobCount)
	repo := &connectionOutageAckRepo{Repo: ts.jobsRepo, acked: acked}
	repo.disconnect = func(faultCtx context.Context) error {
		_, faultErr := control.Exec(faultCtx,
			"ALTER DATABASE "+pgx.Identifier{database}.Sanitize()+" ALLOW_CONNECTIONS false")
		if faultErr != nil {
			return fmt.Errorf("deny fixture connections: %w", faultErr)
		}
		var sessions, terminated int
		faultErr = control.QueryRow(faultCtx, `SELECT count(*), count(*) FILTER (WHERE pg_terminate_backend(pid, 1000))
			FROM pg_stat_activity WHERE datname = $1 AND usename = current_user`, database).Scan(&sessions, &terminated)
		if faultErr != nil {
			return fmt.Errorf("terminate fixture sessions: %w", faultErr)
		}
		if sessions == 0 || terminated != sessions {
			return fmt.Errorf("terminated %d of %d fixture sessions", terminated, sessions)
		}
		return nil
	}
	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(repo),
		outbox.WithJobsFailedRepo(ts.jobsFailedRepo),
		outbox.WithTransactor(transaction.New(ts.db.DB())),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	svc.MustRegisterJob(newJobMock(jobName, func(handlerCtx context.Context, payload string) error {
		id := outbox.JobIDFromContext(handlerCtx)
		if id.IsZero() || expected[id] != payload {
			return errors.New("unexpected recovery job identity or payload")
		}
		attempts[id]++
		lastStarted[id] = time.Now().UTC()
		if attempts[id] == 1 {
			firstStarted[id] = lastStarted[id]
		}
		return nil
	}, time.Second, 3))
	for index := range jobCount {
		payload := fmt.Sprintf("payload-%02d", index)
		id, putErr := svc.Put(ctx, jobName, payload, time.Now().UTC())
		require.NoError(t, putErr)
		expected[id] = payload
	}

	// The real ACK is attempted only after PostgreSQL denies new connections
	// and terminates existing sessions. No synthetic repository error is used.
	err = svc.Run(ctx)
	require.Error(t, err, "the existing runtime must report persistence failure")
	require.NoError(t, ctx.Err(), "failure must precede the test watchdog")
	require.NoError(t, repo.disconnectErr, "fault injection itself must succeed")
	require.Error(t, repo.ackErr, "the real database ACK must fail")
	require.ErrorIs(t, err, repo.ackErr)
	require.Len(t, attempts, 1)
	require.Len(t, acked, 0, "no ACK may be confirmed during the outage")
	require.ErrorIs(t, svc.Readiness(ctx), outbox.ErrServiceNotRunning)
	require.Error(t, pool.Ping(ctx), "the original pool must observe the connection outage")

	_, err = control.Exec(ctx, allowSQL)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return pool.Ping(probeCtx) == nil
	}, 5*time.Second, 50*time.Millisecond, "the original pool must reconnect without replacement")
	queued, err := ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, queued, jobCount, "all persisted work must survive the outage")
	seen := make(map[outbox.JobID]bool, jobCount)
	var interruptedUntil time.Time
	for _, job := range queued {
		require.False(t, seen[job.ID])
		seen[job.ID] = true
		require.Contains(t, expected, job.ID)
		require.Equal(t, expected[job.ID], job.Payload)
		if job.ID == repo.interruptedID {
			require.Equal(t, 1, job.Attempts)
			require.True(t, job.ReservedAt.Valid)
			require.False(t, job.LeaseToken.IsZero())
			interruptedUntil = job.ReservedAt.Time
		} else {
			require.Zero(t, job.Attempts)
		}
	}
	require.False(t, interruptedUntil.IsZero())

	// Run deliberately fails fast on storage errors; the host restarts Run.
	// Reuse the same service, repositories and pool, without resetting leases.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	joined := false
	go func() { done <- svc.Run(runCtx) }()
	defer func() {
		stop()
		if !joined {
			require.NoError(t, <-done)
		}
	}()
	confirmed := make(map[outbox.JobID]bool, jobCount)
	for range jobCount {
		select {
		case id := <-acked:
			require.Contains(t, expected, id)
			require.False(t, confirmed[id], "each persisted job must ACK only once")
			confirmed[id] = true
		case runErr := <-done:
			joined = true
			t.Fatalf("reconnected service stopped before acknowledging the complete backlog: %v", runErr)
		case <-ctx.Done():
			t.Fatal("reconnected service did not acknowledge the complete backlog")
		}
	}
	stop()
	// Join before inspecting handler-owned state.
	err = <-done
	joined = true
	require.NoError(t, err)
	require.Len(t, attempts, jobCount)
	for id := range expected {
		want := 1
		if id == repo.interruptedID {
			want = 2
			require.False(t, lastStarted[id].Before(interruptedUntil), "retry must respect the persisted lease")
			require.True(t, firstStarted[id].Before(interruptedUntil))
		}
		require.Equal(t, want, attempts[id])
	}
	queued, err = ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Empty(t, queued)
	failed, err := ts.jobsFailedRepo.All(ctx)
	require.NoError(t, err)
	require.Empty(t, failed)
}

// The test has one worker and reads these fields only after Run has joined.
type connectionOutageAckRepo struct {
	*jobsrepo.Repo
	disconnect    func(context.Context) error
	disconnectErr error
	ackErr        error
	interruptedID outbox.JobID
	acked         chan outbox.JobID
}

func (r *connectionOutageAckRepo) DeleteJobWithLease(
	ctx context.Context, id outbox.JobID, token outbox.LeaseToken, now time.Time,
) (int64, error) {
	first := r.interruptedID.IsZero()
	if first {
		r.interruptedID = id
		r.disconnectErr = r.disconnect(ctx)
		if r.disconnectErr != nil {
			return 0, r.disconnectErr
		}
	}
	affected, err := r.Repo.DeleteJobWithLease(ctx, id, token, now)
	if first {
		r.ackErr = err
	}
	if err == nil && affected == 1 {
		r.acked <- id
	}
	return affected, err
}
