package outbox

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	leaseBoundFinalize = "finalize"
	leaseBoundForget   = "forget"
)

// Drive the ledger without a heartbeat so refreshes cannot conceal admission
// defects. synctest callers advance expiry without depending on wall time.
func newLeaseBoundTestManager(t *testing.T, deadlines ...time.Duration) (*batchLeaseManager, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	manager := &batchLeaseManager{
		repo: &executionBatchTestRepo{}, leaseToken: types.NewLeaseToken(), reserveFor: 30 * time.Second,
		outstanding: make(map[types.JobID]time.Time), cancelBatch: cancel,
	}
	for _, deadline := range deadlines {
		jobID := types.NewJobID()
		manager.orderedIDs = append(manager.orderedIDs, jobID)
		manager.outstanding[jobID] = time.Now().UTC().Add(deadline)
	}
	manager.recomputeEarliestLocked()
	return manager, ctx
}

func TestLeaseBoundAfterRemovingEarliest(t *testing.T) {
	for _, removal := range []string{leaseBoundFinalize, leaseBoundForget} {
		for _, expired := range []bool{false, true} {
			name := removal + "/live-tail"
			if expired {
				name = removal + "/expired-tail"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tail := 2 * time.Minute
					if expired {
						tail = 20 * time.Second
					}
					manager, ctx := newLeaseBoundTestManager(t, 10*time.Second, tail)
					repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
					repo.extend = func(context.Context, []types.JobID, time.Time, time.Time) (int64, error) {
						t.Fatal("removing an earlier lease must not extend a protected tail or resurrect an expired one")
						return 0, nil
					}
					manager.repo = repo
					if removal == leaseBoundFinalize {
						require.NoError(t, manager.finalize(ctx, manager.orderedIDs[0], func(context.Context) error { return nil }))
					} else {
						manager.forget(manager.orderedIDs[0])
					}
					time.Sleep(21 * time.Second)
					err := manager.admit(ctx, make(chan struct{}))
					if expired {
						require.ErrorIs(t, err, ErrLeaseLost)
						require.ErrorIs(t, context.Cause(ctx), ErrLeaseLost)
					} else {
						require.NoError(t, err)
						require.Equal(t, manager.outstanding[manager.orderedIDs[1]], manager.earliestUntil)
					}
				})
			})
		}
	}
}

func TestLeaseBoundRefreshWithoutExtension(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, ctx := newLeaseBoundTestManager(t, time.Second, time.Minute)
		repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
		repo.extend = func(context.Context, []types.JobID, time.Time, time.Time) (int64, error) {
			t.Fatal("the remaining lease already covers admission and finalization")
			return 0, nil
		}
		manager.repo = repo
		manager.forget(manager.orderedIDs[0])
		require.NoError(t, manager.admit(ctx, make(chan struct{})))
		require.Equal(t, manager.outstanding[manager.orderedIDs[1]], manager.earliestUntil)
		require.NoError(t, manager.finalize(ctx, manager.orderedIDs[1], func(context.Context) error { return nil }))
		require.Empty(t, manager.outstanding)
		require.True(t, manager.earliestUntil.IsZero())
	})
}

func TestLeaseBoundEmptyThenAdd(t *testing.T) {
	for _, removal := range []string{leaseBoundFinalize, leaseBoundForget, "finalize-all", "forget-all", "release"} {
		t.Run(removal, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager, ctx := newLeaseBoundTestManager(t, time.Minute, time.Minute)
				switch removal {
				case leaseBoundFinalize:
					for _, jobID := range manager.orderedIDs {
						require.NoError(t, manager.finalize(ctx, jobID, func(context.Context) error { return nil }))
					}
				case leaseBoundForget:
					for _, jobID := range manager.orderedIDs {
						manager.forget(jobID)
					}
				case "finalize-all":
					require.NoError(t, manager.finalizeAll(ctx, time.Now().Add(10*time.Second), func() error { return nil }))
				case "forget-all":
					manager.forgetAll()
				case "release":
					require.NoError(t, manager.releaseUnstarted(ctx))
				}
				require.Empty(t, manager.outstanding)
				require.True(t, manager.earliestUntil.IsZero())
				job := executionBatchTestJob(testBatchJobName, manager.leaseToken)
				job.ReservedAt.Time = time.Now().UTC().Add(20 * time.Second)
				require.NoError(t, manager.add([]models.Job{job}))
				require.Equal(t, job.ReservedAt.Time, manager.earliestUntil)
				time.Sleep(21 * time.Second)
				require.ErrorIs(t, manager.admit(ctx, make(chan struct{})), ErrLeaseLost)
			})
		})
	}
}

func TestLeaseBoundAddingEarlierJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, ctx := newLeaseBoundTestManager(t, time.Minute)
		job := executionBatchTestJob(testBatchJobName, manager.leaseToken)
		job.ReservedAt.Time = time.Now().UTC().Add(20 * time.Second)
		require.NoError(t, manager.add([]models.Job{job}))
		time.Sleep(21 * time.Second)
		require.ErrorIs(t, manager.admit(ctx, make(chan struct{})), ErrLeaseLost)
	})
}

func TestLeaseBoundFailedFinalizationRetainsJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, ctx := newLeaseBoundTestManager(t, time.Second, time.Minute)
		jobID := manager.orderedIDs[0]
		finalizeErr := errors.New("ack failed")
		extensions := 0
		repo := &leaseExtensionTestRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
		repo.extend = func(_ context.Context, ids []types.JobID, _, until time.Time) (int64, error) {
			extensions++
			require.Equal(t, []types.JobID{jobID}, ids)
			require.True(t, until.After(time.Now().Add(30*time.Second)))
			return int64(len(ids)), nil
		}
		manager.repo = repo
		require.ErrorIs(t, manager.finalize(ctx, jobID, func(context.Context) error { return finalizeErr }), finalizeErr)
		require.Len(t, manager.outstanding, 2)
		require.NoError(t, manager.finalize(ctx, jobID, func(context.Context) error { return nil }))
		require.Equal(t, 1, extensions)
		require.Len(t, manager.outstanding, 1)
		require.NoError(t, manager.admit(ctx, make(chan struct{})))
	})
}
