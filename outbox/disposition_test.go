package outbox_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
)

func TestPermanentPreservesCauseAndIsIdempotent(t *testing.T) {
	cause := errors.New("invalid payload")
	err := outbox.Permanent(cause)
	require.ErrorIs(t, err, cause)
	require.True(t, outbox.IsPermanent(fmt.Errorf("handle: %w", err)))
	require.Equal(t, err, outbox.Permanent(err))
	require.NoError(t, outbox.Permanent(nil))
}

func TestRetryAtPreservesCauseAndUTCInstant(t *testing.T) {
	cause := errors.New("unavailable")
	wanted := time.Date(2026, 8, 23, 12, 30, 0, 123, time.FixedZone("test", 5*60*60))
	err := outbox.RetryAt(cause, wanted)
	require.ErrorIs(t, err, cause)
	got, ok := outbox.RetryTime(fmt.Errorf("handle: %w", err))
	require.True(t, ok)
	require.Equal(t, wanted.UTC(), got)
	require.NoError(t, outbox.RetryAt(nil, wanted))
	_, ok = outbox.RetryTime(cause)
	require.False(t, ok)
}
