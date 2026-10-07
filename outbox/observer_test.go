package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func TestObserverPublicConsumerWiring(t *testing.T) {
	// The supported consumer path exposes all observer types; no shared imports.
	events := make(chan outbox.RuntimeOutcome, 4)
	repo := newCapabilityRepo()
	svc, err := outbox.New(
		outbox.WithJobsRepo(repo), outbox.WithJobsFailedRepo(repo), outbox.WithTransactor(repo),
		outbox.WithObserver(events), outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	svc.MustRegisterJob(capabilityJob{name: "observed", version: 1})
	id, err := svc.Put(t.Context(), "observed", "private payload", time.Now().UTC())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	select {
	case event := <-events:
		require.Equal(t, outbox.OutcomeACKConfirmed, event.Kind)
		require.Equal(t, id, event.JobID)
		require.Empty(t, repo.Jobs(), "storage must confirm ACK before notification")
	case <-ctx.Done():
		t.Fatal("no confirmed ACK observed")
	}
	svc.BeginDrain()
	require.NoError(t, <-done)
	close(events) // Caller closes only after Run joins all senders.
}
