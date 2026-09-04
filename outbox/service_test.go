package outbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	outboxmocks "github.com/assurrussa/outbox/outbox/mocks"
)

type testAtomicMockTransactor struct {
	*outboxmocks.MockTransactor
}

func (testAtomicMockTransactor) SupportsAtomicDLQ() bool {
	return true
}

func TestCreate_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockTransactor := testAtomicMockTransactor{MockTransactor: outboxmocks.NewMockTransactor(ctrl)}
	mockJobsRepo := outboxmocks.NewMockJobsRepository(ctrl)
	mockJobsRepo.EXPECT().MaxReservationBatchSize().Return(outbox.MaxReservationBatchSize)
	mockJobsStatRepo := outboxmocks.NewMockJobsStatRepository(ctrl)
	mockJobsFailedRepo := outboxmocks.NewMockJobsFailedRepository(ctrl)

	srv, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(1*time.Second),
		outbox.WithReserveFor(5*time.Minute),
		outbox.WithLogger(logger.Discard()),
		outbox.WithTransactor(mockTransactor),
		outbox.WithJobsRepo(mockJobsRepo),
		outbox.WithJobsStatRepo(mockJobsStatRepo),
		outbox.WithJobsFailedRepo(mockJobsFailedRepo),
	)
	require.NoError(t, err)
	assert.NotNil(t, srv)
}

func TestCreate_Error(t *testing.T) {
	srv, err := outbox.New(
		outbox.WithWorkers(0),
		outbox.WithIdleTime(0),
		outbox.WithReserveFor(9999*time.Minute),
		outbox.WithLogger(nil),
		outbox.WithTransactor(nil),
		outbox.WithJobsRepo(nil),
		outbox.WithJobsStatRepo(nil),
		outbox.WithJobsFailedRepo(nil),
	)
	require.ErrorIs(t, err, outbox.ErrOption)
	assert.Nil(t, srv)
}

func TestCreate_WorkerCount(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		wantErr bool
	}{
		{name: "negative", workers: -1, wantErr: true},
		{name: "zero", workers: 0, wantErr: true},
		{name: testValueOne, workers: 1},
		{name: "host defined above former limit", workers: 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			jobsRepo := outboxmocks.NewMockJobsRepository(ctrl)
			if tt.workers > 0 {
				jobsRepo.EXPECT().MaxReservationBatchSize().Return(outbox.MaxReservationBatchSize)
			}
			srv, err := outbox.New(
				outbox.WithWorkers(tt.workers),
				outbox.WithLogger(logger.Discard()),
				outbox.WithTransactor(testAtomicMockTransactor{MockTransactor: outboxmocks.NewMockTransactor(ctrl)}),
				outbox.WithJobsRepo(jobsRepo),
				outbox.WithJobsFailedRepo(outboxmocks.NewMockJobsFailedRepository(ctrl)),
			)

			if tt.wantErr {
				require.ErrorContains(t, err, "invalid number of workers")
				assert.Nil(t, srv)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, srv)
		})
	}
}
