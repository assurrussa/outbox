//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

func activeJobsCount(ctx context.Context, repo outbox.JobsStatRepository) (int64, error) {
	stats, err := repo.GetQueueStats(ctx, time.Now().UTC())
	return stats.Total, err
}

func TestSQLiteQueueStatsGroupsReadyScheduledAndProcessing(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	oldest := observedAt.Add(-2 * time.Minute)
	_, err := ts.jobsRepo.CreateJobVersioned(ctx, "alpha", 1, `{}`, oldest)
	require.NoError(t, err)
	_, err = ts.jobsRepo.CreateJobVersioned(ctx, "alpha", 1, `{}`, observedAt.Add(-time.Minute))
	require.NoError(t, err)
	_, err = ts.jobsRepo.CreateJobVersioned(ctx, "alpha", 1, `{}`, observedAt.Add(time.Hour))
	require.NoError(t, err)
	_, err = ts.jobsRepo.CreateJobVersioned(ctx, "beta", 2, `{}`, observedAt.Add(-time.Minute))
	require.NoError(t, err)
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx,
		observedAt,
		observedAt.Add(time.Minute),
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "beta", SchemaVersion: 2}},
		1,
	)
	require.NoError(t, err)

	stats, err := ts.jobsRepo.GetQueueStats(ctx, observedAt)
	require.NoError(t, err)
	require.Equal(t, observedAt, stats.ObservedAt)
	require.Equal(t, int64(4), stats.Total)
	require.Equal(t, int64(2), stats.Available)
	require.Equal(t, int64(1), stats.Processing)
	require.Len(t, stats.ByCapability, 2)
	require.Equal(t, outbox.CapabilityQueueStats{
		Name: "alpha", SchemaVersion: 1, Total: 3, Available: 2,
		OldestAvailableAt: oldest,
	}, stats.ByCapability[0])
	require.Equal(t, outbox.CapabilityQueueStats{
		Name: "beta", SchemaVersion: 2, Total: 1, Processing: 1,
	}, stats.ByCapability[1])
}
