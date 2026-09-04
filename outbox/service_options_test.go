package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	outboxmocks "github.com/assurrussa/outbox/outbox/mocks"
)

type plainTransactor struct{}

func (plainTransactor) RunInTx(ctx context.Context, f func(context.Context) error) error {
	return f(ctx)
}

type transactorWithCapability struct {
	atomicDLQ bool
}

func (t transactorWithCapability) RunInTx(ctx context.Context, f func(context.Context) error) error {
	return f(ctx)
}

func (t transactorWithCapability) SupportsAtomicDLQ() bool {
	return t.atomicDLQ
}

func TestTransactionCapabilitiesValidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	jobsRepo := outboxmocks.NewMockJobsRepository(ctrl)
	jobsRepo.EXPECT().MaxReservationBatchSize().Return(outbox.MaxReservationBatchSize).AnyTimes()
	jobsFailedRepo := outboxmocks.NewMockJobsFailedRepository(ctrl)

	baseOpts := []outbox.OptOptionsSetter{
		outbox.WithWorkers(1),
		outbox.WithIdleTime(200 * time.Millisecond),
		outbox.WithReserveFor(5 * time.Second),
		outbox.WithLogger(logger.Discard()),
		outbox.WithJobsRepo(jobsRepo),
		outbox.WithJobsFailedRepo(jobsFailedRepo),
	}

	t.Run("transactor without capabilities without opt-in returns ErrTransactionCapabilitiesRequired", func(t *testing.T) {
		opts := append(append([]outbox.OptOptionsSetter{}, baseOpts...),
			outbox.WithTransactor(plainTransactor{}),
		)
		_, err := outbox.New(opts...)
		require.ErrorIs(t, err, outbox.ErrTransactionCapabilitiesRequired)
	})

	t.Run("transactor without capabilities with WithAllowNonAtomicDLQ succeeds", func(t *testing.T) {
		opts := append(append([]outbox.OptOptionsSetter{}, baseOpts...),
			outbox.WithTransactor(plainTransactor{}),
			outbox.WithAllowNonAtomicDLQ(),
		)
		svc, err := outbox.New(opts...)
		require.NoError(t, err)
		require.NotNil(t, svc)
	})

	t.Run("transactor with atomic DLQ without opt-in succeeds", func(t *testing.T) {
		opts := append(append([]outbox.OptOptionsSetter{}, baseOpts...),
			outbox.WithTransactor(transactorWithCapability{atomicDLQ: true}),
		)
		svc, err := outbox.New(opts...)
		require.NoError(t, err)
		require.NotNil(t, svc)
	})

	t.Run("transactor without atomic DLQ without opt-in returns ErrNonAtomicDLQUnsupported", func(t *testing.T) {
		opts := append(append([]outbox.OptOptionsSetter{}, baseOpts...),
			outbox.WithTransactor(transactorWithCapability{atomicDLQ: false}),
		)
		_, err := outbox.New(opts...)
		require.ErrorIs(t, err, outbox.ErrNonAtomicDLQUnsupported)
	})

	t.Run("transactor without atomic DLQ with WithAllowNonAtomicDLQ succeeds", func(t *testing.T) {
		opts := append(append([]outbox.OptOptionsSetter{}, baseOpts...),
			outbox.WithTransactor(transactorWithCapability{atomicDLQ: false}),
			outbox.WithAllowNonAtomicDLQ(),
		)
		svc, err := outbox.New(opts...)
		require.NoError(t, err)
		require.NotNil(t, svc)
	})
}
