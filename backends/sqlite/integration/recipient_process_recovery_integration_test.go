//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/migrator"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/sqlite/storage"
	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

const sqliteRecipientProcessEnv = "OUTBOX_TEST_SQLITE_RECIPIENT_PROCESS_ROOT"

// This fixture models a recipient whose idempotency ledger and business effect
// commit in a separate database. It does not claim that Outbox makes arbitrary
// external effects exactly once, or simulate a machine/storage power failure.
func TestSQLiteRecipientEffectSurvivesProcessKillBeforeAck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	queuePath := filepath.Join(root, "queue.db")
	recipientPath := filepath.Join(root, "recipient.db")
	queue := openSQLiteRecipientFixture(t, ctx, queuePath)
	recipient := openSQLiteRecipientFixture(t, ctx, recipientPath)
	require.NoError(t, migrator.RunEmbedded(ctx, queue.DB(), logger.Discard(), migrator.WithCommand("up")))
	_, err := recipient.DB().ExecContext(ctx, `
		CREATE TABLE effects (delivery_id TEXT PRIMARY KEY, target_id TEXT NOT NULL, payload TEXT NOT NULL);
		CREATE TABLE attempts (delivery_id TEXT NOT NULL, attempted_at INTEGER NOT NULL);
		CREATE TABLE effect_total (id INTEGER PRIMARY KEY CHECK (id = 1), total INTEGER NOT NULL);
		INSERT INTO effect_total (id, total) VALUES (1, 0);
	`)
	require.NoError(t, err)

	jobs := jobsrepo.Must(queue)
	producer := newSQLiteRecipientService(t, queue, jobs, recipient.DB())
	event := sqliteFanoutEvent()
	event.SchemaVersion = 1
	const targetCount = 8
	targets := make([]outbox.FanoutTarget, targetCount)
	expected := make(map[string]string, targetCount)
	for index := range targets {
		targets[index] = outbox.FanoutTarget{Kind: "webhook", ID: fmt.Sprintf("target-%02d", index)}
		expected[outbox.FanoutDeliveryID(event.ID, targets[index].Kind, targets[index].ID).String()] = targets[index].ID
	}
	_, err = producer.PutFanout(ctx, event, targets, time.Now().UTC())
	require.NoError(t, err)
	// The child must recover real file-backed state, with no inherited DB handle.
	require.NoError(t, queue.Close())
	require.NoError(t, recipient.Close())

	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteRecipientEffectProcessHelper$", "-test.timeout=45s")
	command.Env = append(os.Environ(), sqliteRecipientProcessEnv+"="+root)
	command.WaitDelay = 5 * time.Second
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoError(t, command.Start())
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		if t.Failed() {
			t.Logf("recipient child output:\n%s", output.String())
		}
	}()

	var interruptedJobID outbox.JobID
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(filepath.Join(root, "before-ack"))
		if readErr != nil {
			return false
		}
		return interruptedJobID.UnmarshalText(data) == nil
	}, 20*time.Second, 10*time.Millisecond, "child never reached the effect-committed/pre-ACK boundary")
	require.NoError(t, ctx.Err(), "parent must request the kill before its watchdog expires")
	require.NoError(t, command.Process.Kill())
	err = command.Wait()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Success(), "the worker must be killed rather than shut down normally")
	require.NotContains(t, output.String(), "WARNING: DATA RACE", "a killed child must not hide a race report")

	queue = openSQLiteRecipientFixture(t, ctx, queuePath)
	recipient = openSQLiteRecipientFixture(t, ctx, recipientPath)
	jobs = jobsrepo.Must(queue)
	interrupted, err := jobs.GetByID(ctx, interruptedJobID)
	require.NoError(t, err)
	require.True(t, interrupted.ReservedAt.Valid)
	require.False(t, interrupted.LeaseToken.IsZero())
	require.Equal(t, 1, interrupted.Attempts, "the committed recipient effect follows the first persisted claim")
	delivery, err := outbox.DecodeFanoutDelivery(interrupted.Payload)
	require.NoError(t, err)
	require.Contains(t, expected, delivery.ID.String())
	assertSQLiteRecipientCounts(t, ctx, recipient.DB(), 1, 1)
	var committedID string
	require.NoError(t, recipient.DB().QueryRowContext(ctx, "SELECT delivery_id FROM effects").Scan(&committedID))
	require.Equal(t, delivery.ID.String(), committedID)
	queued, err := jobs.GetQueueStats(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(targetCount), queued.Total, "dispatcher ACK must precede the recipient fault")

	// Start immediately: the real worker must respect the persisted lease and
	// reclaim the interrupted delivery when it expires, while draining siblings.
	restarted := newSQLiteRecipientService(t, queue, jobs, recipient.DB())
	require.True(t, time.Now().UTC().Before(interrupted.ReservedAt.Time), "restart must begin with an unexpired lease")
	stop := startSQLiteRecipientService(t, ctx, restarted)
	require.Eventually(t, func() bool {
		stats, statsErr := jobs.GetQueueStats(ctx, time.Now().UTC())
		return statsErr == nil && stats.Total == 0
	}, 20*time.Second, 10*time.Millisecond, "restarted worker did not drain the durable backlog")
	stop()
	failed, err := jobsfailedrepo.Must(queue).CountExact(ctx)
	require.NoError(t, err)
	require.Zero(t, failed)
	assertSQLiteRecipientCounts(t, ctx, recipient.DB(), targetCount, targetCount+1)
	var retriedAt int64
	require.NoError(t, recipient.DB().QueryRowContext(ctx,
		"SELECT MAX(attempted_at) FROM attempts WHERE delivery_id = ?", delivery.ID.String(),
	).Scan(&retriedAt))
	require.GreaterOrEqual(t, retriedAt, interrupted.ReservedAt.Time.UnixMilli(), "recovery must wait for the persisted lease")

	rows, err := recipient.DB().QueryContext(ctx, `
		SELECT effects.delivery_id, effects.target_id, effects.payload, COUNT(attempts.delivery_id)
		FROM effects JOIN attempts ON attempts.delivery_id = effects.delivery_id
		GROUP BY effects.delivery_id, effects.target_id, effects.payload
	`)
	require.NoError(t, err)
	defer rows.Close()
	seen := make(map[string]string, targetCount)
	for rows.Next() {
		var id, target, payload string
		var attempts int
		require.NoError(t, rows.Scan(&id, &target, &payload, &attempts))
		require.Equal(t, string(event.Payload), payload)
		wantAttempts := 1
		if id == delivery.ID.String() {
			wantAttempts = 2
		}
		require.Equal(t, wantAttempts, attempts, "committed recipient attempt count for delivery %s", id)
		seen[id] = target
	}
	require.NoError(t, rows.Err())
	require.Equal(t, expected, seen)
}

// This entry point is invoked only by the test-owned subprocess above. Normal
// integration traversal leaves it inert; the parent owns its files and lifetime.
func TestSQLiteRecipientEffectProcessHelper(t *testing.T) {
	root := os.Getenv(sqliteRecipientProcessEnv)
	if root == "" {
		return
	}
	ctx := context.Background()
	queue := openSQLiteRecipientFixture(t, ctx, filepath.Join(root, "queue.db"))
	recipient := openSQLiteRecipientFixture(t, ctx, filepath.Join(root, "recipient.db"))
	jobs := &sqlitePauseRecipientAckRepo{Repo: jobsrepo.Must(queue), marker: filepath.Join(root, "before-ack")}
	service := newSQLiteRecipientService(t, queue, jobs, recipient.DB())
	require.NoError(t, service.Run(ctx))
	t.Fatal("recipient worker returned without being killed at its ACK boundary")
}

func openSQLiteRecipientFixture(t *testing.T, ctx context.Context, path string) *storage.ClientSQLite {
	t.Helper()
	client, err := storage.Create(ctx, path,
		storage.WithMaxOpenConns(1), storage.WithMaxIdleConns(1),
		storage.WithSynchronousMode(storage.SynchronousFull), storage.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func newSQLiteRecipientService(
	t *testing.T,
	queue *storage.ClientSQLite,
	jobs outbox.JobsRepository,
	recipient *sql.DB,
) *outbox.Service {
	t.Helper()
	service, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobs),
		outbox.WithFanoutJobsRepo(jobsrepo.Must(queue)),
		outbox.WithJobsFailedRepo(jobsfailedrepo.Must(queue)),
		outbox.WithTransactor(transaction.New(queue.DB())),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	require.NoError(t, service.RegisterJob(&sqliteDurableRecipient{db: recipient}))
	return service
}

func startSQLiteRecipientService(t *testing.T, ctx context.Context, service *outbox.Service) func() {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- service.Run(runCtx) }()
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Error("recipient service did not stop before fixture cleanup")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

type sqlitePauseRecipientAckRepo struct {
	*jobsrepo.Repo
	marker string
}

func (r *sqlitePauseRecipientAckRepo) DeleteJobWithLease(
	ctx context.Context,
	jobID outbox.JobID,
	token outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	job, err := r.Repo.GetByID(ctx, jobID)
	if err != nil {
		return 0, err
	}
	if job.Name == outbox.FanoutDispatcherJobName {
		return r.Repo.DeleteJobWithLease(ctx, jobID, token, now)
	}
	if err := os.WriteFile(r.marker, []byte(jobID.String()), 0o600); err != nil {
		return 0, err
	}
	// Deliberately ignore cancellation at this injected boundary: only the
	// parent's process kill (or its bounded command context) may release it.
	select {}
}

type sqliteDurableRecipient struct {
	db *sql.DB
}

func (*sqliteDurableRecipient) Name() string {
	return outbox.FanoutDeliveryJobName("webhook", "cms.entry.published")
}

func (*sqliteDurableRecipient) ExecutionTimeout() time.Duration { return 5 * time.Second }

func (*sqliteDurableRecipient) MaxAttempts() int { return 3 }

func (r *sqliteDurableRecipient) Handle(ctx context.Context, payload string) error {
	attemptedAt := time.Now().UTC().UnixMilli()
	delivery, err := outbox.DecodeFanoutDelivery(payload)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO effects (delivery_id, target_id, payload) VALUES (?, ?, ?)
		ON CONFLICT(delivery_id) DO NOTHING
	`, delivery.ID.String(), delivery.Target.ID, string(delivery.Event.Payload))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	// Reject identity reuse with changed content rather than treating it as an
	// idempotent success. The real host/recipient owns this boundary.
	var target, body string
	if err := tx.QueryRowContext(ctx,
		"SELECT target_id, payload FROM effects WHERE delivery_id = ?", delivery.ID.String(),
	).Scan(&target, &body); err != nil {
		return err
	}
	if target != delivery.Target.ID || body != string(delivery.Event.Payload) {
		return errors.New("recipient effect identity conflicts with prior content")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE effect_total SET total = total + ? WHERE id = 1", inserted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO attempts (delivery_id, attempted_at) VALUES (?, ?)", delivery.ID.String(), attemptedAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func assertSQLiteRecipientCounts(t *testing.T, ctx context.Context, db *sql.DB, effects, attempts int) {
	t.Helper()
	var actualEffects, actualAttempts, actualTotal int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM effects").Scan(&actualEffects))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM attempts").Scan(&actualAttempts))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT total FROM effect_total WHERE id = 1").Scan(&actualTotal))
	require.Equal(t, effects, actualEffects)
	require.Equal(t, attempts, actualAttempts)
	require.Equal(t, effects, actualTotal, "business effect must commit once per stable delivery identity")
}
