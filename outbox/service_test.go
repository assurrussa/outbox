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

func TestCreate_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockTransactor := outboxmocks.NewMockTransactor(ctrl)
	mockJobsRepo := outboxmocks.NewMockJobsRepository(ctrl)
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
