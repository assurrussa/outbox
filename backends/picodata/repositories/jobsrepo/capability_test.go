package jobsrepo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
)

func TestRepositoryDoesNotAdvertiseFanoutWithoutAtomicTransactions(t *testing.T) {
	var repo any = (*Repo)(nil)
	_, ok := repo.(outbox.FanoutJobsRepository)
	require.False(t, ok)
}
