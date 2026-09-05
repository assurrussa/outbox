package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	leaseClaimSingleton = "singleton"
	leaseReleaseError   = "release error"
	leaseDraining       = "draining"
	leaseLateSuccess    = "late success"
	leaseClaimOrdinary  = "ordinary"
	leaseClaimBounded   = "bounded"
	leaseExpired        = "expired"
	leaseCancelled      = "cancelled"
)

func newLeaseClaimService(
	t *testing.T,
	path string,
	claim func(context.Context, LeaseToken) ([]models.Job, error),
) (*Service, *atomic.Int32, func(context.Context) error) {
	t.Helper()
	base := &executionBatchTestRepo{}
	base.findSingle = func(ctx context.Context, token LeaseToken, _ []JobCapability) ([]models.Job, error) {
		return claim(ctx, token)
	}
	base.findBatch = func(ctx context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
		return claim(ctx, token)
	}
	var repo JobsRepository = base
	if path == leaseClaimBounded {
		bounded := &boundedExecutionBatchTestRepo{executionBatchTestRepo: base}
		bounded.findBounded = func(ctx context.Context, _ JobCapability, token LeaseToken, _ BatchClaimLimits) ([]models.Job, error) {
			return claim(ctx, token)
		}
		repo = bounded
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.reserveFor = time.Second
	calls := &atomic.Int32{}
	capability := JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion}
	if path == leaseClaimOrdinary {
		service.MustRegisterJob(&executionSingleTestHandler{name: testBatchJobName, after: func(string) { calls.Add(1) }})
		return service, calls, func(ctx context.Context) error {
			return service.findAndProcessBatch(ctx, logger.Discard(), []JobCapability{capability})
		}
	}
	service.MustRegisterBatchJob(&executionBatchTestHandler{
		name: testBatchJobName, after: func(string) { calls.Add(1) },
	}, BatchConfig{MaxMessages: 1})
	return service, calls, func(ctx context.Context) error {
		_, err := service.findAndProcessExecutionBatch(ctx, logger.Discard(), capability)
		return err
	}
}

func TestClaimsRejectInvalidOrLateLease(t *testing.T) {
	for _, path := range []string{leaseClaimOrdinary, leaseClaimSingleton, leaseClaimBounded} {
		for _, defect := range []string{leaseExpired, "missing deadline", "wrong token", leaseLateSuccess} {
			t.Run(path+"/"+defect, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					_, calls, process := newLeaseClaimService(t, path, func(ctx context.Context, token LeaseToken) ([]models.Job, error) {
						deadline, ok := ctx.Deadline()
						require.True(t, ok)
						require.Equal(t, time.Second, time.Until(deadline))
						job := executionBatchTestJob(testBatchJobName, token)
						switch defect {
						case leaseExpired:
							job.ReservedAt.Time = time.Now().Add(-time.Second)
						case "missing deadline":
							job.ReservedAt.Valid = false
						case "wrong token":
							job.LeaseToken = types.NewLeaseToken()
						case leaseLateSuccess:
							<-ctx.Done()
						}
						return []models.Job{job}, nil
					})
					err := process(t.Context())
					if defect == leaseLateSuccess {
						require.ErrorIs(t, err, context.DeadlineExceeded)
					} else {
						require.ErrorIs(t, err, ErrLeaseLost)
					}
					require.Zero(t, calls.Load())
				})
			})
		}
	}
}

func TestDrainTimerCancelsBlockedClaims(t *testing.T) {
	for _, path := range []string{leaseClaimOrdinary, leaseClaimSingleton, leaseClaimBounded} {
		t.Run(path, func(t *testing.T) {
			entered := make(chan struct{})
			service, calls, process := newLeaseClaimService(t, path, func(ctx context.Context, _ LeaseToken) ([]models.Job, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			runCtx, cancelRun := context.WithCancel(t.Context())
			defer cancelRun()
			done := make(chan error, 1)
			go func() { done <- process(runCtx) }()
			<-entered
			startedAt := time.Now()
			timer := time.AfterFunc(100*time.Millisecond, cancelRun)
			defer timer.Stop()
			service.BeginDrain()
			require.Less(t, time.Since(startedAt), time.Second)
			require.ErrorIs(t, <-done, context.Canceled)
			require.Zero(t, calls.Load())
			require.True(t, service.IsDraining())
		})
	}
}

type leaseExtensionTestRepo struct {
	*executionBatchTestRepo
	extend func(context.Context, []types.JobID, time.Time, time.Time) (int64, error)
}

func (r *leaseExtensionTestRepo) ExtendJobLeases(
	ctx context.Context, ids []types.JobID, _ LeaseToken, now, until time.Time,
) (int64, error) {
	return r.extend(ctx, ids, now, until)
}

func TestAdmissionConfirmsLeaseBeforeHandler(t *testing.T) {
	for _, outcome := range []string{"renewed", leaseExpired, "slow extension", "partial extension", leaseCancelled, leaseDraining} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				token := types.NewLeaseToken()
				job := executionBatchTestJob(testBatchJobName, token)
				job.ReservedAt.Time = time.Now().Add(100 * time.Millisecond)
				batchCtx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				drain := make(chan struct{})
				extensions := 0
				repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
				repo.extend = func(_ context.Context, ids []types.JobID, _, until time.Time) (int64, error) {
					extensions++
					require.Equal(t, []types.JobID{job.ID}, ids)
					require.True(t, until.After(job.ReservedAt.Time))
					switch outcome {
					case "slow extension":
						time.Sleep(2 * time.Second)
					case "partial extension":
						return 0, nil
					case leaseCancelled:
						cancel(context.Canceled)
					case leaseDraining:
						close(drain)
					}
					return int64(len(ids)), nil
				}
				// Drive admission directly so a periodic heartbeat cannot conceal a
				// missed or delayed renewal in the admission path itself.
				manager := &batchLeaseManager{
					repo: repo, leaseToken: token, reserveFor: time.Second,
					orderedIDs: []types.JobID{job.ID}, outstanding: map[types.JobID]time.Time{job.ID: job.ReservedAt.Time},
					cancelBatch: cancel,
				}
				if outcome == leaseExpired {
					time.Sleep(200 * time.Millisecond)
				}
				err := manager.admit(batchCtx, drain)
				switch outcome {
				case "renewed":
					require.NoError(t, err)
					require.True(t, manager.outstanding[job.ID].After(time.Now()))
				case leaseDraining:
					require.ErrorIs(t, err, ErrServiceDraining)
				case leaseCancelled:
					require.ErrorIs(t, err, context.Canceled)
				default:
					require.ErrorIs(t, err, ErrLeaseLost)
				}
				if outcome == leaseExpired {
					require.Zero(t, extensions, "an expired lease must not be resurrected")
				} else {
					require.Equal(t, 1, extensions)
				}
			})
		})
	}
}

func TestSingleFinalizationProtectsTailAndSharesDeadline(t *testing.T) {
	token := types.NewLeaseToken()
	jobs := []models.Job{executionBatchTestJob(testBatchJobName, token), executionBatchTestJob(testBatchJobName, token)}
	for i := range jobs {
		jobs[i].ReservedAt.Time = time.Now().Add(time.Second)
	}
	batchCtx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	var extendedUntil time.Time
	repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
	repo.extend = func(_ context.Context, ids []types.JobID, _, until time.Time) (int64, error) {
		require.Len(t, ids, 2)
		extendedUntil = until
		time.Sleep(200 * time.Millisecond)
		return int64(len(ids)), nil
	}
	manager := newBatchLeaseManager(batchCtx, repo, jobs, token, time.Second, cancel)
	startedAt := time.Now()
	err := manager.finalize(batchCtx, jobs[0].ID, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, startedAt.Add(leaseFinalizationTimeout), deadline, 20*time.Millisecond)
		require.GreaterOrEqual(t, extendedUntil.Sub(deadline), batchFinalizationMargin)
		time.Sleep(2 * time.Second)
		require.True(t, extendedUntil.After(time.Now()), "tail must remain unavailable to another owner")
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, manager.admit(batchCtx, make(chan struct{})))
	require.NoError(t, manager.stopAndWait())
	require.Equal(t, extendedUntil, manager.outstanding[jobs[1].ID], "heartbeat must not shorten the protected tail")
}

func TestFinalizationFailsClosedOnPartialExtension(t *testing.T) {
	batchCtx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	token := types.NewLeaseToken()
	job := executionBatchTestJob(testBatchJobName, token)
	job.ReservedAt.Time = time.Now().Add(time.Second)
	repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
	repo.extend = func(context.Context, []types.JobID, time.Time, time.Time) (int64, error) { return 0, nil }
	manager := newBatchLeaseManager(batchCtx, repo, []models.Job{job}, token, time.Second, cancel)
	called := false
	err := manager.finalize(batchCtx, job.ID, func(context.Context) error { called = true; return nil })
	require.ErrorIs(t, err, ErrLeaseLost)
	require.False(t, called)
	require.ErrorIs(t, context.Cause(batchCtx), ErrLeaseLost)
	require.ErrorIs(t, manager.stopAndWait(), ErrLeaseLost)
}

func TestLeaseProtectionSkipsRowsWithEnoughTime(t *testing.T) {
	batchCtx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	token := types.NewLeaseToken()
	short := executionBatchTestJob(testBatchJobName, token)
	short.ReservedAt.Time = time.Now().Add(time.Second)
	long := executionBatchTestJob(testBatchJobName, token)
	repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
	calls := 0
	repo.extend = func(_ context.Context, ids []types.JobID, _, _ time.Time) (int64, error) {
		calls++
		require.Equal(t, []types.JobID{short.ID}, ids)
		return int64(len(ids)), nil
	}
	manager := newBatchLeaseManager(batchCtx, repo, []models.Job{short, long}, token, time.Second, cancel)
	err := manager.finalize(batchCtx, short.ID, func(context.Context) error { return nil })
	require.NoError(t, err)
	err = manager.finalize(batchCtx, long.ID, func(context.Context) error { return nil })
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.NoError(t, manager.stopAndWait())
}

func TestSequentialFinalizationDoesNotExtendEveryRow(t *testing.T) {
	batchCtx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	token := types.NewLeaseToken()
	short1 := executionBatchTestJob(testBatchJobName, token)
	short1.ReservedAt.Time = time.Now().Add(time.Second)
	short2 := executionBatchTestJob(testBatchJobName, token)
	short2.ReservedAt.Time = time.Now().Add(time.Second)
	repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
	calls := 0
	repo.extend = func(_ context.Context, ids []types.JobID, _, _ time.Time) (int64, error) {
		calls++
		return int64(len(ids)), nil
	}
	manager := newBatchLeaseManager(batchCtx, repo, []models.Job{short1, short2}, token, time.Second, cancel)
	err := manager.finalize(batchCtx, short1.ID, func(context.Context) error { return nil })
	require.NoError(t, err)
	err = manager.finalize(batchCtx, short2.ID, func(context.Context) error { return nil })
	require.NoError(t, err)
	require.Equal(t, 1, calls, "second finalization must not redundantly extend the lease")
	require.NoError(t, manager.stopAndWait())
}

func TestCancelledSuccessfulClaimReleasesUnstartedJobs(t *testing.T) {
	for _, path := range []string{leaseClaimOrdinary, leaseClaimSingleton, leaseClaimBounded} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var claimedID types.JobID
			service, calls, process := newLeaseClaimService(t, path, func(_ context.Context, token LeaseToken) ([]models.Job, error) {
				job := executionBatchTestJob(testBatchJobName, token)
				claimedID = job.ID
				cancel()
				return []models.Job{job}, nil
			})
			var repo *executionBatchTestRepo
			if bounded, ok := service.jobsRepo.(*boundedExecutionBatchTestRepo); ok {
				repo = bounded.executionBatchTestRepo
			} else {
				var ok bool
				repo, ok = service.jobsRepo.(*executionBatchTestRepo)
				require.True(t, ok)
			}
			var released []types.JobID
			repo.onRelease = func(ids []types.JobID) { released = append(released, ids...) }

			require.ErrorIs(t, process(ctx), context.Canceled)
			require.Zero(t, calls.Load())
			require.Equal(t, []types.JobID{claimedID}, released, "a confirmed unstarted claim must compensate its attempt")
		})
	}
}

func TestFinalizationDeadlineBoundsLeaseMutexWait(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			token := types.NewLeaseToken()
			job := executionBatchTestJob(testBatchJobName, token)
			manager := newBatchLeaseManager(ctx, &executionBatchTestRepo{}, []models.Job{job}, token, time.Minute, cancel)
			manager.mu.Lock() // A slow heartbeat owns the lease ledger.
			done := make(chan error, 1)
			var finalizations atomic.Int32
			go func() {
				if batch {
					finalizeCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
					defer stop()
					deadline, _ := finalizeCtx.Deadline()
					done <- manager.finalizeAll(finalizeCtx, deadline.Add(time.Second), func() error {
						finalizations.Add(1)
						return nil
					})
					return
				}
				done <- manager.finalize(ctx, job.ID, func(context.Context) error {
					finalizations.Add(1)
					return nil
				})
			}()
			wait := leaseFinalizationTimeout + time.Second
			if batch {
				wait = 500 * time.Millisecond
			}
			var err error
			bounded := true
			select {
			case err = <-done:
			case <-time.After(wait):
				bounded = false
			}
			manager.mu.Unlock()
			if !bounded {
				err = <-done
			}
			require.NoError(t, manager.stopAndWait())
			require.True(t, bounded, "waiting for the heartbeat lock exceeded the finalization budget")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Zero(t, finalizations.Load())
		})
	}
}

type claimCleanupTestRepo struct {
	*executionBatchTestRepo
	cleanup func(context.Context, []types.JobID, LeaseToken, time.Time) (int64, error)
}

func (r *claimCleanupTestRepo) ReleaseUnstartedJobsWithLease(
	ctx context.Context, ids []types.JobID, token LeaseToken, now time.Time,
) (int64, error) {
	return r.cleanup(ctx, ids, token, now)
}

func TestCancelledClaimCleanupPreservesOwnershipAndDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	token := types.NewLeaseToken()
	owned := executionBatchTestJob(testBatchJobName, token)
	foreign := executionBatchTestJob(testBatchJobName, types.NewLeaseToken())
	expired := executionBatchTestJob(testBatchJobName, token)
	expired.ReservedAt.Time = time.Now().Add(-time.Second)
	repo := &claimCleanupTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	repo.cleanup = func(cleanupCtx context.Context, ids []types.JobID, gotToken LeaseToken, _ time.Time) (int64, error) {
		require.NoError(t, cleanupCtx.Err())
		deadline, ok := cleanupCtx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), leaseFinalizationTimeout)
		require.Equal(t, []types.JobID{owned.ID}, ids)
		require.Equal(t, token, gotToken)
		drained := make(chan struct{})
		go func() {
			service.BeginDrain()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(time.Second):
			return 0, errors.New("claim cleanup still holds the drain lock")
		}
		return 1, nil
	}
	jobs, err := service.claimJobsWithLease(ctx, token, func(context.Context, time.Time, time.Time) ([]models.Job, error) {
		cancel()
		return []models.Job{owned, foreign, expired, owned}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, errCancelledClaimCleanup)
	require.Empty(t, jobs)
	require.True(t, service.IsDraining())
}

func TestLateFillClaimCleanupIsNotHiddenByMaxWait(t *testing.T) {
	cleanupErr := errors.New("release failed")
	for _, outcome := range []string{"released", "partial release", leaseReleaseError} {
		t.Run(outcome, func(t *testing.T) {
			repo := &claimCleanupTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
			repo.findBatch = func(ctx context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
				<-ctx.Done()
				return []models.Job{executionBatchTestJob(testBatchJobName, token)}, nil
			}
			cleanups := 0
			repo.cleanup = func(ctx context.Context, ids []types.JobID, _ LeaseToken, _ time.Time) (int64, error) {
				cleanups++
				require.NoError(t, ctx.Err())
				if outcome == leaseReleaseError {
					return 0, cleanupErr
				}
				if outcome == "partial release" {
					return 0, nil
				}
				return int64(len(ids)), nil
			}
			service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
			jobs, flushed, err := service.claimExecutionBatchWithinFillWindow(t.Context(), repo,
				JobCapability{Name: testBatchJobName, SchemaVersion: 1}, types.NewLeaseToken(),
				BatchClaimLimits{MaxMessages: 1, MaxBytes: 1024}, time.Now().Add(50*time.Millisecond))
			require.Empty(t, jobs)
			require.Equal(t, 1, cleanups)
			if outcome == "released" {
				require.NoError(t, err)
				require.True(t, flushed)
				return
			}
			require.False(t, flushed)
			require.ErrorIs(t, err, errCancelledClaimCleanup)
			if outcome == leaseReleaseError {
				require.ErrorIs(t, err, cleanupErr)
			} else {
				require.ErrorIs(t, err, ErrLeaseLost)
			}
		})
	}
}
