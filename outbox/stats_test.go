package outbox_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func TestJobsRepositoryAutodetectsStatsAndSortsCapabilitySnapshot(t *testing.T) {
	base := newCapabilityRepo()
	auto := &statsJobsRepo{
		capabilityRepo: base,
		stats: outbox.QueueStats{
			ObservedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.FixedZone("stale", 3600)),
			Total:      3,
			Available:  2,
			Processing: 1,
			ByCapability: []outbox.CapabilityQueueStats{
				{
					Name: "beta", SchemaVersion: 2, Total: 1, Processing: 1,
				},
				{
					Name: "alpha", SchemaVersion: 1, Total: 2, Available: 2,
					OldestAvailableAt: time.Date(2026, 8, 28, 9, 0, 0, 0, time.FixedZone("source", 7200)),
				},
			},
		},
	}
	svc, err := outbox.New(
		outbox.WithJobsRepo(auto),
		outbox.WithJobsFailedRepo(base),
		outbox.WithTransactor(base),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	before := time.Now().UTC()
	stats, err := svc.GetQueueStats(t.Context())
	after := time.Now().UTC()
	require.NoError(t, err)
	require.True(t, auto.called.Load())
	require.False(t, stats.ObservedAt.Before(before))
	require.False(t, stats.ObservedAt.After(after))
	require.Equal(t, time.UTC, stats.ObservedAt.Location())
	require.Equal(t, []string{"alpha", "beta"}, []string{
		stats.ByCapability[0].Name,
		stats.ByCapability[1].Name,
	})
	require.Equal(t, time.UTC, stats.ByCapability[0].OldestAvailableAt.Location())
}

func TestExplicitStatsRepositoryOverridesAutodetection(t *testing.T) {
	base := newCapabilityRepo()
	auto := &statsJobsRepo{capabilityRepo: base, stats: outbox.QueueStats{Total: 1}}
	explicit := &fixedStatsRepo{stats: outbox.QueueStats{Total: 2}}
	svc, err := outbox.New(
		outbox.WithJobsRepo(auto),
		outbox.WithJobsStatRepo(explicit),
		outbox.WithJobsFailedRepo(base),
		outbox.WithTransactor(base),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	stats, err := svc.GetQueueStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Total)
	require.True(t, explicit.called.Load())
	require.False(t, auto.called.Load())
}

type statsJobsRepo struct {
	*capabilityRepo
	stats  outbox.QueueStats
	called atomic.Bool
}

func (r *statsJobsRepo) GetQueueStats(_ context.Context, _ time.Time) (outbox.QueueStats, error) {
	r.called.Store(true)
	return r.stats, nil
}

type fixedStatsRepo struct {
	stats  outbox.QueueStats
	called atomic.Bool
}

func (r *fixedStatsRepo) GetQueueStats(_ context.Context, _ time.Time) (outbox.QueueStats, error) {
	r.called.Store(true)
	return r.stats, nil
}

var (
	_ outbox.JobsRepository     = (*statsJobsRepo)(nil)
	_ outbox.JobsStatRepository = (*statsJobsRepo)(nil)
	_ outbox.JobsStatRepository = (*fixedStatsRepo)(nil)
)
