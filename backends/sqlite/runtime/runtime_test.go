package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/migrator"
	sqliteruntime "github.com/assurrussa/outbox/backends/sqlite/runtime"
	"github.com/assurrussa/outbox/backends/sqlite/storage"
)

type recordingRetryPolicy struct {
	delay    time.Duration
	mu       sync.Mutex
	attempts []int
}

func (p *recordingRetryPolicy) RetryDelay(attempt coreoutbox.RetryAttempt) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = append(p.attempts, attempt.Attempt)
	return p.delay
}

func (p *recordingRetryPolicy) Attempts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, len(p.attempts))
	copy(out, p.attempts)
	return out
}

type flakyRuntimeJob struct {
	coreoutbox.DefaultJob
	calls atomic.Int32
}

func (j *flakyRuntimeJob) Name() string { return "runtime.retry" }

func (j *flakyRuntimeJob) MaxAttempts() int { return 3 }

func (j *flakyRuntimeJob) Handle(_ context.Context, _ string) error {
	if j.calls.Add(1) == 1 {
		return errors.New("transient error")
	}
	return nil
}

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	_, err := sqliteruntime.Open(context.Background(), sqliteruntime.Config{})
	require.Error(t, err)
}

func TestOpenSynchronousMode(t *testing.T) {
	for _, mode := range []storage.SynchronousMode{"", storage.SynchronousNormal, storage.SynchronousFull} {
		runtime, err := sqliteruntime.Open(t.Context(), sqliteruntime.Config{
			DSN:             filepath.Join(t.TempDir(), "runtime.db"),
			SynchronousMode: mode,
		})
		require.NoError(t, err)
		var actual int
		require.NoError(t, runtime.Client().DB().QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&actual))
		want := 1
		if mode == storage.SynchronousFull {
			want = 2
		}
		require.Equal(t, want, actual)
		require.NoError(t, runtime.Close())
	}
	_, err := sqliteruntime.Open(t.Context(), sqliteruntime.Config{
		DSN: filepath.Join(t.TempDir(), "invalid.db"), SynchronousMode: "OFF",
	})
	require.ErrorContains(t, err, "synchronous mode must be NORMAL or FULL")
}

func TestRuntimeHonorsRetryPolicyAndObserver(t *testing.T) {
	const customRetryDelay = 120 * time.Millisecond
	policy := &recordingRetryPolicy{delay: customRetryDelay}
	outcomes := make(chan coreoutbox.RuntimeOutcome, 4)

	runtime, err := sqliteruntime.Open(t.Context(), sqliteruntime.Config{
		DSN:         filepath.Join(t.TempDir(), "behavior.db"),
		Workers:     1,
		IdleTime:    100 * time.Millisecond,
		ReserveFor:  time.Second,
		RetryPolicy: policy,
		Observer:    outcomes,
		Logger:      logger.Discard(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	require.NoError(t, migrator.RunEmbedded(
		t.Context(),
		runtime.Client().DB(),
		logger.Discard(),
		migrator.WithCommand("up"),
	))

	job := &flakyRuntimeJob{}
	runtime.Service().MustRegisterJob(job)

	enqueuedAt := time.Now().UTC().Truncate(time.Millisecond)
	jobID, err := runtime.Service().Put(t.Context(), job.Name(), `{}`, enqueuedAt)
	require.NoError(t, err)

	runCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(runCtx) }()

	var first coreoutbox.RuntimeOutcome
	select {
	case first = <-outcomes:
	case <-runCtx.Done():
		t.Fatal("timed out waiting for retry outcome")
	}
	require.Equal(t, jobID, first.JobID)
	require.Equal(t, coreoutbox.OutcomeRetryPersisted, first.Kind)
	require.Equal(t, 1, first.Attempt)
	require.False(t, first.AvailableAt.Before(enqueuedAt.Add(customRetryDelay)))

	var second coreoutbox.RuntimeOutcome
	select {
	case second = <-outcomes:
	case <-runCtx.Done():
		t.Fatal("timed out waiting for ack outcome")
	}
	require.Equal(t, jobID, second.JobID)
	require.Equal(t, coreoutbox.OutcomeACKConfirmed, second.Kind)
	require.Equal(t, 2, second.Attempt)
	require.Equal(t, []int{1}, policy.Attempts())

	cancel()
	require.NoError(t, <-done)
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *sqliteruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
