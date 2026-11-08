package runtime_test

import (
	"context"
	"testing"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/stretchr/testify/require"

	mysqlruntime "github.com/assurrussa/outbox/backends/mysql/runtime"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	policy, err := coreoutbox.NewExponentialRetryPolicy(time.Second, time.Minute, nil)
	require.NoError(t, err)
	observer := make(chan coreoutbox.RuntimeOutcome, 1)
	_, err = mysqlruntime.Open(context.Background(), mysqlruntime.Config{
		RetryPolicy: policy,
		Observer:    observer,
	})
	require.Error(t, err)
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *mysqlruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
