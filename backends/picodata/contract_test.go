package picodata_test

import (
	"testing"

	"github.com/assurrussa/outbox/outbox"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/picodata/storage"
)

var _ picodata.ClientTransaction = (*storage.ClientPicoData)(nil)

func TestConstructionRequiresNonAtomicDLQOptIn(t *testing.T) {
	// Construction validates capabilities without querying the zero-value client.
	client := &storage.ClientPicoData{}
	options := []outbox.OptOptionsSetter{
		outbox.WithJobsRepo(jobsrepo.Must(client)),
		outbox.WithJobsFailedRepo(jobsfailedrepo.Must(client)),
		outbox.WithTransactor(client.TxPool()),
	}
	_, err := outbox.New(options...)
	require.ErrorIs(t, err, outbox.ErrNonAtomicDLQUnsupported)
	options = append(options, outbox.WithAllowNonAtomicDLQ())
	_, err = outbox.New(options...)
	require.NoError(t, err)
	_, err = outbox.New(append(options, outbox.WithReservationBatchSize(2))...)
	require.ErrorIs(t, err, outbox.ErrReservationBatchSizeUnsupported)
}
