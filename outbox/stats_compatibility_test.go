package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/sharederrors"
)

func TestServiceQueueStatsImplementsStats(t *testing.T) {
	const (
		firstName = "stats-a"
		lastName  = "stats-b"
	)
	base := newCapabilityRepo()
	oldest := time.Date(2026, 8, 28, 9, 0, 0, 0, time.FixedZone("source", 7200))
	auto := &statsJobsRepo{
		capabilityRepo: base,
		stats: outbox.QueueStats{
			ObservedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.FixedZone("stale", 3600)),
			Total:      4,
			Available:  2,
			Processing: 1,
			ByCapability: []outbox.CapabilityQueueStats{
				{Name: lastName, SchemaVersion: 1, Total: 1},
				{Name: firstName, SchemaVersion: 2, Total: 1, Processing: 1},
				{Name: firstName, SchemaVersion: 1, Total: 2, Available: 2, OldestAvailableAt: oldest},
			},
		},
	}
	svc := newStatsCompatibilityService(t, base, outbox.WithJobsRepo(auto))
	var provider outbox.Stats = svc

	before := time.Now().UTC()
	stats, err := provider.QueueStats(t.Context())
	after := time.Now().UTC()
	require.NoError(t, err)
	require.True(t, auto.called.Load())
	require.False(t, stats.ObservedAt.Before(before))
	require.False(t, stats.ObservedAt.After(after))
	require.Equal(t, time.UTC, stats.ObservedAt.Location())
	require.Equal(t, outbox.QueueStats{
		ObservedAt: stats.ObservedAt,
		Total:      4,
		Available:  2,
		Processing: 1,
		ByCapability: []outbox.CapabilityQueueStats{
			{Name: firstName, SchemaVersion: 1, Total: 2, Available: 2, OldestAvailableAt: oldest.UTC()},
			{Name: firstName, SchemaVersion: 2, Total: 1, Processing: 1},
			{Name: lastName, SchemaVersion: 1, Total: 1},
		},
	}, stats)
}

func TestServiceQueueStatsExplicitRepositoryOverridesAutodetection(t *testing.T) {
	for _, explicitFirst := range []bool{false, true} {
		name := "explicit last"
		if explicitFirst {
			name = "explicit first"
		}
		t.Run(name, func(t *testing.T) {
			base := newCapabilityRepo()
			auto := &statsJobsRepo{capabilityRepo: base, stats: outbox.QueueStats{Total: 1}}
			explicit := &fixedStatsRepo{stats: outbox.QueueStats{Total: 2}}
			options := []outbox.OptOptionsSetter{
				outbox.WithJobsRepo(auto),
				outbox.WithJobsStatRepo(explicit),
			}
			if explicitFirst {
				options[0], options[1] = options[1], options[0]
			}
			svc := newStatsCompatibilityService(t, base, options...)

			stats, err := svc.QueueStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, int64(2), stats.Total)
			require.True(t, explicit.called.Load())
			require.False(t, auto.called.Load())
		})
	}
}

func TestServiceQueueStatsMethodsWithoutRepository(t *testing.T) {
	svc := newStatsCompatibilityService(t, newCapabilityRepo())
	for name, read := range queueStatsMethods(svc) {
		t.Run(name, func(t *testing.T) {
			stats, err := read(t.Context())
			require.ErrorIs(t, err, sharederrors.ErrJobStatNotInit)
			require.Same(t, sharederrors.ErrJobStatNotInit, err)
			require.Equal(t, outbox.QueueStats{}, stats)
		})
	}
}

func TestServiceQueueStatsMethodsForwardContextAndObservationTime(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "caller")
	var receivedContext context.Context
	var observedAt time.Time
	calls := 0
	repo := queueStatsRepositoryFunc(func(gotContext context.Context, gotTime time.Time) (outbox.QueueStats, error) {
		receivedContext = gotContext
		observedAt = gotTime
		calls++
		return outbox.QueueStats{Total: 7}, nil
	})
	svc := newStatsCompatibilityService(t, newCapabilityRepo(), outbox.WithJobsStatRepo(repo))
	for name, read := range queueStatsMethods(svc) {
		t.Run(name, func(t *testing.T) {
			calls = 0
			before := time.Now().UTC()
			stats, err := read(ctx)
			after := time.Now().UTC()
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Same(t, ctx, receivedContext)
			require.Equal(t, time.UTC, observedAt.Location())
			require.False(t, observedAt.Before(before))
			require.False(t, observedAt.After(after))
			require.Equal(t, observedAt, stats.ObservedAt)
			require.Equal(t, int64(7), stats.Total)
		})
	}
}

func TestServiceQueueStatsMethodsPreserveRepositoryErrors(t *testing.T) {
	for _, cause := range []error{errors.New("stats unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			repo := queueStatsRepositoryFunc(func(context.Context, time.Time) (outbox.QueueStats, error) {
				return outbox.QueueStats{Total: 7}, cause
			})
			svc := newStatsCompatibilityService(t, newCapabilityRepo(), outbox.WithJobsStatRepo(repo))
			for name, read := range queueStatsMethods(svc) {
				t.Run(name, func(t *testing.T) {
					stats, err := read(t.Context())
					require.ErrorIs(t, err, cause)
					require.EqualError(t, err, "get queue stats: "+cause.Error())
					require.Equal(t, outbox.QueueStats{}, stats)
				})
			}
		})
	}
}

func TestServiceQueueStatsMethodsForwardCanceledContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "canceled"
		if expired {
			name = "deadline exceeded"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if expired {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer cancel()
			}
			require.Error(t, ctx.Err())
			var receivedContext context.Context
			calls := 0
			repo := queueStatsRepositoryFunc(func(gotContext context.Context, _ time.Time) (outbox.QueueStats, error) {
				receivedContext = gotContext
				calls++
				return outbox.QueueStats{}, gotContext.Err()
			})
			svc := newStatsCompatibilityService(t, newCapabilityRepo(), outbox.WithJobsStatRepo(repo))
			for method, read := range queueStatsMethods(svc) {
				t.Run(method, func(t *testing.T) {
					calls = 0
					stats, err := read(ctx)
					require.Equal(t, 1, calls)
					require.Same(t, ctx, receivedContext)
					require.ErrorIs(t, err, ctx.Err())
					require.Equal(t, outbox.QueueStats{}, stats)
				})
			}
		})
	}
}

func newStatsCompatibilityService(
	t *testing.T,
	base *capabilityRepo,
	extra ...outbox.OptOptionsSetter,
) *outbox.Service {
	t.Helper()
	options := []outbox.OptOptionsSetter{
		outbox.WithJobsRepo(base),
		outbox.WithJobsFailedRepo(base),
		outbox.WithTransactor(base),
		outbox.WithLogger(logger.Discard()),
	}
	svc, err := outbox.New(append(options, extra...)...)
	require.NoError(t, err)
	return svc
}

func queueStatsMethods(svc *outbox.Service) map[string]func(context.Context) (outbox.QueueStats, error) {
	var provider outbox.Stats = svc
	return map[string]func(context.Context) (outbox.QueueStats, error){
		"Stats.QueueStats":      provider.QueueStats,
		"Service.GetQueueStats": svc.GetQueueStats,
	}
}

type queueStatsRepositoryFunc func(context.Context, time.Time) (outbox.QueueStats, error)

func (f queueStatsRepositoryFunc) GetQueueStats(ctx context.Context, observedAt time.Time) (outbox.QueueStats, error) {
	return f(ctx, observedAt)
}
