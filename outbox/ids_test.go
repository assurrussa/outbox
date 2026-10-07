package outbox_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
)

// These signatures compile using only the supported consumer import path.
var (
	_ func(*outbox.Service, context.Context, string, string, time.Time) (outbox.JobID, error) = (*outbox.Service).Put
	_ func(context.Context) outbox.JobID                                                      = outbox.JobIDFromContext
	_ func(outbox.MessageID, string, string) outbox.MessageID                                 = outbox.FanoutDeliveryID
)

func TestPublicIDs(t *testing.T) {
	job := outbox.NewJobID()
	message := outbox.NewMessageID()
	require.False(t, job.IsZero())
	require.False(t, message.IsZero())
	parsedJob, err := outbox.ParseJobID(job.String())
	require.NoError(t, err)
	require.Equal(t, job, parsedJob)
	parsedMessage, err := outbox.ParseMessageID(message.String())
	require.NoError(t, err)
	require.Equal(t, message, parsedMessage)
	_, err = outbox.ParseJobID("invalid")
	require.Error(t, err)
	_, err = outbox.ParseMessageID("invalid")
	require.Error(t, err)
	zeroJob, err := outbox.ParseJobID((outbox.JobID{}).String())
	require.NoError(t, err)
	require.True(t, zeroJob.IsZero())
	require.Error(t, zeroJob.Validate())
	zeroMessage, err := outbox.ParseMessageID((outbox.MessageID{}).String())
	require.NoError(t, err)
	require.True(t, zeroMessage.IsZero())
	require.Error(t, zeroMessage.Validate())
}

func TestPublicIDEncoding(t *testing.T) {
	message := outbox.NewMessageID()
	event := outbox.FanoutEvent{ID: message}
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	var decoded outbox.FanoutEvent
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, message, decoded.ID)
	job := outbox.NewJobID()
	var scanned outbox.JobID
	require.NoError(t, scanned.Scan(job.String()))
	require.Equal(t, job, scanned)
	value, err := job.Value()
	require.NoError(t, err)
	require.Equal(t, job.String(), value)
}
