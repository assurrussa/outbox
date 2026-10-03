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

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
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

func TestWorkerGoexitJoinsHeartbeat(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := &goexitHeartbeatRepo{
					executionBatchTestRepo: &executionBatchTestRepo{},
					started:                make(chan struct{}), canceled: make(chan struct{}),
					allowExit: make(chan struct{}), exited: make(chan struct{}),
				}
				claim := func(token LeaseToken) ([]models.Job, error) {
					job := executionBatchTestJob(testBatchJobName, token)
					job.ReservedAt.Time = time.Now().Add(3 * time.Second)
					return []models.Job{job}, nil
				}
				repo.findSingle = func(_ context.Context, token LeaseToken, _ []JobCapability) ([]models.Job, error) {
					return claim(token)
				}
				repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
					return claim(token)
				}
				service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
				service.reserveFor = 3 * time.Second
				exit := func() { <-repo.started; runtime.Goexit() }
				if batch {
					service.MustRegisterBatchJob(&executionBatchTestHandler{
						name:   testBatchJobName,
						handle: func(context.Context, []BatchJobItem) (BatchResult, error) { exit(); return BatchResult{}, nil },
					}, BatchConfig{MaxMessages: 1})
				} else {
					service.MustRegisterJob(&executionSingleTestHandler{name: testBatchJobName, after: func(string) { exit() }})
				}
				done := make(chan error, 1)
				go func() { done <- service.Run(t.Context()) }()
				<-repo.canceled
				synctest.Wait()
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
			})
		})
	}
}
