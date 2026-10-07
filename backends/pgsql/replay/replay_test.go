package replay_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/replay"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
)

type borrowedClient struct{ engine storage.DBEngine }

func (c borrowedClient) DB() storage.DBEngine { return c.engine }
func (borrowedClient) Close() error           { return nil }

// Embedding supplies unused methods. A validation regression reaching the DB
// fails immediately instead of silently passing against a forgiving mock.
type (
	noQueries struct{ storage.DBEngine }
	markerTx  struct{ pgx.Tx }
)

func TestReplayValidationBeforeDatabase(t *testing.T) {
	_, err := replay.New(nil)
	require.ErrorIs(t, err, replay.ErrNotConfigured)
	_, err = replay.New(borrowedClient{})
	require.ErrorIs(t, err, replay.ErrNotConfigured)
	r, err := replay.New(borrowedClient{engine: noQueries{}})
	require.NoError(t, err)
	id, err := replay.ParseJobID(replay.NewRequestID().String())
	require.NoError(t, err)
	request := replay.Request{RequestID: replay.NewRequestID(), FailedJobID: id}
	admit := func(context.Context, replay.Source) (string, error) { return "original-effect", nil }
	for _, invalid := range []replay.Request{{}, {RequestID: request.RequestID}, {FailedJobID: id}} {
		result, err := r.Replay(t.Context(), invalid, admit)
		require.ErrorIs(t, err, replay.ErrInvalidRequest)
		require.Zero(t, result)
	}
	result, err := r.Replay(t.Context(), request, nil)
	require.ErrorIs(t, err, replay.ErrAdmissionRequired)
	require.Zero(t, result)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = r.Replay(ctx, request, admit)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, result)
	result, err = r.Replay(storage.WithTx(t.Context(), markerTx{}), request, admit)
	require.ErrorIs(t, err, replay.ErrNestedTransaction)
	require.Zero(t, result)
	var unconfigured *replay.Replayer
	result, err = unconfigured.Replay(t.Context(), request, admit)
	require.ErrorIs(t, err, replay.ErrNotConfigured)
	require.Zero(t, result)
	_, err = unconfigured.ByJobID(t.Context(), id)
	require.ErrorIs(t, err, replay.ErrNotConfigured)
	_, err = r.ByRequestID(t.Context(), replay.RequestID{})
	require.ErrorIs(t, err, replay.ErrInvalidRequest)
	_, err = r.ByJobID(t.Context(), replay.JobID{})
	require.ErrorIs(t, err, replay.ErrInvalidRequest)
}

func TestRequestIDs(t *testing.T) {
	id := replay.NewRequestID()
	require.False(t, id.IsZero())
	parsed, err := replay.ParseRequestID(id.String())
	require.NoError(t, err)
	require.Equal(t, id, parsed)
	_, err = replay.ParseRequestID("invalid")
	require.Error(t, err)
	_, err = replay.ParseJobID("invalid")
	require.Error(t, err)
}
