package transaction_test

import (
	"context"
	"errors"
	"testing"

	picogo "github.com/picodata/picodata-go"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
)

func TestBestEffortCallbackDoesNotInstallTransaction(t *testing.T) {
	// RunInTx only checks that a pool exists; no network method is called.
	runner := transaction.New(&picogo.Pool{})
	var legacy *transaction.Manager = runner
	require.False(t, legacy.SupportsAtomicDLQ())
	callbackErr := errors.New("owned callback failure")
	calls := 0
	ctx := t.Context()
	err := legacy.RunInTx(ctx, func(actual context.Context) error {
		calls++
		require.Equal(t, ctx, actual)
		require.Nil(t, transaction.GetTx(actual))
		return callbackErr
	})
	require.ErrorIs(t, err, callbackErr)
	require.Equal(t, 1, calls)
}

func TestUnconfiguredRunnerRejectsCallback(t *testing.T) {
	for _, runner := range []*transaction.BestEffortRunner{nil, transaction.New(nil)} {
		called := false
		err := runner.RunInTx(t.Context(), func(context.Context) error { called = true; return nil })
		require.ErrorContains(t, err, "transaction manager is not configured")
		require.False(t, called)
		require.False(t, runner.SupportsAtomicDLQ())
	}
}
