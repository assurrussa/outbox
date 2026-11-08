package jobsrepo_test

import (
	"testing"

	"github.com/assurrussa/outbox/outbox"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsrepo"
)

func TestRepositoryDoesNotAdvertiseAtomicCapabilities(t *testing.T) {
	var repo any = (*jobsrepo.Repo)(nil)
	_, ok := repo.(outbox.FanoutJobsRepository)
	require.False(t, ok)
	_, batch := repo.(outbox.BatchJobsRepository)
	require.False(t, batch)
	require.Equal(t, 1, (*jobsrepo.Repo)(nil).MaxReservationBatchSize())
}
