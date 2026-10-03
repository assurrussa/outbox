package outbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	workerExitHandlerErrorPath = "handler error"
	workerExitPanicPath        = "panic"
	workerExitAdmissionPath    = "admission"
	workerExitCancellationPath = "cancellation"
)

func TestWorkerGoexitStopsAllWorkersWithoutAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		batch   bool
		workers int
	}{{false, 1}, {false, 2}, {true, 1}, {true, 2}} {
		batch, workers := test.batch, test.workers
		t.Run(fmt.Sprintf("batch=%t/workers=%d", batch, workers), func(t *testing.T) {
			repo := &executionBatchTestRepo{}
			var claimed atomic.Bool
			claim := func(token LeaseToken) ([]models.Job, error) {
				if claimed.CompareAndSwap(false, true) {
					return []models.Job{executionBatchTestJob(testBatchJobName, token)}, nil
				}
				return nil, ErrNoJobs
			}
			repo.findSingle = func(_ context.Context, token LeaseToken, _ []JobCapability) ([]models.Job, error) {
				return claim(token)
			}
			repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
				return claim(token)
			}
			service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
			service.workers = workers
			if batch {
				service.MustRegisterBatchJob(&executionBatchTestHandler{
					name: testBatchJobName,
					handle: func(context.Context, []BatchJobItem) (BatchResult, error) {
						runtime.Goexit()
						return BatchResult{}, nil
					},
				}, BatchConfig{MaxMessages: 1})
			} else {
				service.MustRegisterJob(&executionSingleTestHandler{name: testBatchJobName, after: func(string) { runtime.Goexit() }})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- service.Run(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrWorkerGoexit) {
					t.Fatalf("Run error=%v, want worker exit", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("worker exit did not stop Run and its sibling workers")
			}
			if ctx.Err() != nil {
				t.Fatal("Run canceled its caller's context")
			}
			if !errors.Is(service.Readiness(ctx), ErrServiceNotRunning) {
				t.Fatal("service still reports readiness")
			}
			if repo.applyCalls.Load() != 0 || repo.deleteCalls.Load() != 0 || repo.releaseCalls.Load() != 0 {
				t.Fatal("aborted handler's claimed row was finalized or released")
			}
		})
	}
}

type goexitHeartbeatRepo struct {
	*executionBatchTestRepo
	started, canceled, allowExit, exited chan struct{}
}

func (r *goexitHeartbeatRepo) ExtendJobLeases(
	ctx context.Context, _ []types.JobID, _ LeaseToken, _, _ time.Time,
) (int64, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	<-r.allowExit
	close(r.exited)
	return 0, ctx.Err()
}

func TestWorkerGoexitCancelsPeersBeforeJoiningHeartbeat(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				assertWorkerGoexitCancelsPeersBeforeJoiningHeartbeat(t, batch)
			})
		})
	}
}

func assertWorkerGoexitCancelsPeersBeforeJoiningHeartbeat(t *testing.T, batch bool) {
	t.Helper()
	repo := &goexitHeartbeatRepo{
		executionBatchTestRepo: &executionBatchTestRepo{},
		started:                make(chan struct{}), canceled: make(chan struct{}),
		allowExit: make(chan struct{}), exited: make(chan struct{}),
	}
	var claimed atomic.Bool
	peerStarted := make(chan struct{})
	peerCanceled := make(chan struct{})
	claim := func(ctx context.Context, token LeaseToken) ([]models.Job, error) {
		if !claimed.CompareAndSwap(false, true) {
			close(peerStarted)
			<-ctx.Done()
			close(peerCanceled)
			return nil, ctx.Err()
		}
		job := executionBatchTestJob(testBatchJobName, token)
		job.ReservedAt.Time = time.Now().Add(3 * time.Second)
		return []models.Job{job}, nil
	}
	repo.findSingle = func(ctx context.Context, token LeaseToken, _ []JobCapability) ([]models.Job, error) {
		return claim(ctx, token)
	}
	repo.findBatch = func(ctx context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
		return claim(ctx, token)
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.workers = 2
	service.reserveFor = 3 * time.Second
	exit := func() { <-peerStarted; <-repo.started; runtime.Goexit() }
	if batch {
		service.MustRegisterBatchJob(&executionBatchTestHandler{
			name:   testBatchJobName,
			handle: func(context.Context, []BatchJobItem) (BatchResult, error) { exit(); return BatchResult{}, nil },
		}, BatchConfig{MaxMessages: 1})
	} else {
		service.MustRegisterJob(&executionSingleTestHandler{name: testBatchJobName, after: func(string) { exit() }})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	<-repo.canceled
	synctest.Wait()
	select {
	case <-peerCanceled:
	default:
		t.Error("heartbeat cleanup delayed cancellation of a sibling worker")
	}
	if ctx.Err() != nil {
		t.Error("Run canceled its caller's context")
	}
	select {
	case err := <-done:
		close(repo.allowExit)
		t.Fatalf("Run returned before heartbeat exited: %v", err)
	default:
	}
	close(repo.allowExit)
	if err := <-done; !errors.Is(err, ErrWorkerGoexit) {
		t.Fatalf("Run error: %v", err)
	}
	select {
	case <-repo.exited:
	default:
		t.Fatal("heartbeat not joined")
	}
	if repo.applyCalls.Load() != 0 || repo.deleteCalls.Load() != 0 || repo.releaseCalls.Load() != 0 {
		t.Fatal("aborted handler's claimed row was finalized or released")
	}
}

func TestExecutionBatchOrdinaryReturnsDoNotReportWorkerExit(t *testing.T) {
	for _, path := range []string{
		"success", workerExitHandlerErrorPath, workerExitPanicPath, "fill error",
		workerExitAdmissionPath, workerExitCancellationPath, "finalization",
	} {
		t.Run(path, func(t *testing.T) {
			assertExecutionBatchOrdinaryReturn(t, path)
		})
	}
}

func assertExecutionBatchOrdinaryReturn(t *testing.T, path string) {
	t.Helper()
	failure := errors.New("ordinary callback error")
	repo := &executionBatchTestRepo{}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var notified bool
	ctx = context.WithValue(ctx, workerExitContextKey{}, func() { notified = true })
	claims := 0
	repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
		claims++
		if claims > 1 {
			return nil, failure
		}
		job := executionBatchTestJob(testBatchJobName, token)
		if path == workerExitAdmissionPath {
			job.ReservedAt.Time = time.Now().Add(time.Second)
		}
		return []models.Job{job}, nil
	}
	repo.onExtend = func([]types.JobID) { service.BeginDrain() }
	if path == "finalization" {
		repo.applyErr = failure
	}
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			switch path {
			case workerExitHandlerErrorPath:
				return BatchResult{}, failure
			case workerExitPanicPath:
				panic(failure)
			case workerExitCancellationPath:
				cancel()
			}
			return successfulExecutionBatchResult(items), nil
		},
	}
	config := BatchConfig{MaxMessages: 1}
	if path == "fill error" {
		config.MaxMessages = 2
	}
	service.MustRegisterBatchJob(handler, config)
	processed, err := service.findAndProcessExecutionBatch(
		ctx, logger.Discard(), JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if !processed || notified {
		t.Fatalf("ordinary return: processed=%t, exit notification=%t, error=%v", processed, notified, err)
	}
	switch path {
	case "success", workerExitHandlerErrorPath:
		if err != nil {
			t.Fatalf("ordinary handled outcome failed: %v", err)
		}
	case workerExitPanicPath:
		var panicErr *HandlerPanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("recovered panic error: %v", err)
		}
	case workerExitAdmissionPath:
		if !errors.Is(err, ErrServiceDraining) {
			t.Fatalf("admission error: %v", err)
		}
	case workerExitCancellationPath:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	default:
		if !errors.Is(err, failure) {
			t.Fatalf("ordinary callback error: %v", err)
		}
	}
}
