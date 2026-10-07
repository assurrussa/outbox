package jobsrepo

import (
	"testing"

	"github.com/assurrussa/outbox/outbox"
	"github.com/stretchr/testify/require"
)

func TestRepositoryDoesNotAdvertiseAtomicCapabilities(t *testing.T) {
	var repo any = (*Repo)(nil)
	_, ok := repo.(outbox.FanoutJobsRepository)
	require.False(t, ok)
	_, batch := repo.(outbox.BatchJobsRepository)
	require.False(t, batch)
	require.Equal(t, 1, (*Repo)(nil).MaxReservationBatchSize())
}
