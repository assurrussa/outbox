package outbox_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	testFanoutWebhookKind = "webhook"
	testSubscriptionA     = "subscription-a"
)

func TestPutFanoutCanonicalizesTargetsAndRejectsSnapshotDrift(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newFanoutService(t, repo)
	event := newFanoutEvent()
	availableAt := time.Now().UTC().Truncate(time.Microsecond)
	targets := []outbox.FanoutTarget{
		{Kind: testFanoutWebhookKind, ID: "subscription-b", Snapshot: json.RawMessage(`{"revision":2}`)},
		{Kind: "nitro", ID: "site", Snapshot: json.RawMessage(`{"namespace":"public"}`)},
		{Kind: testFanoutWebhookKind, ID: testSubscriptionA, Snapshot: json.RawMessage(`{"revision":1}`)},
	}

	firstID, err := svc.PutFanout(context.Background(), event, targets, availableAt)
	require.NoError(t, err)
	secondID, err := svc.PutFanout(
		context.Background(),
		event,
		[]outbox.FanoutTarget{targets[2], targets[1], targets[0]},
		availableAt,
	)
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)
	require.Len(t, repo.Jobs(), 1)

	drifted := append([]outbox.FanoutTarget(nil), targets...)
	drifted[0].Snapshot = json.RawMessage(`{"revision":3}`)
	_, err = svc.PutFanout(context.Background(), event, drifted, availableAt)
	require.ErrorIs(t, err, outbox.ErrIdempotencyConflict)
}

func TestFanoutDispatcherCreatesIndependentPendingDeliveries(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newFanoutService(t, repo)
	event := newFanoutEvent()
	targets := []outbox.FanoutTarget{
		{Kind: testFanoutWebhookKind, ID: "subscription-b", Snapshot: json.RawMessage(`{"revision":2}`)},
		{Kind: "nitro", ID: "site", Snapshot: json.RawMessage(`{"namespace":"public"}`)},
		{Kind: testFanoutWebhookKind, ID: testSubscriptionA, Snapshot: json.RawMessage(`{"revision":1}`)},
	}

	_, err := svc.PutFanout(context.Background(), event, targets, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))

	jobs := repo.Jobs()
	require.Len(t, jobs, 3)
	deliveryIDs := make(map[types.MessageID]struct{}, len(jobs))
	for _, job := range jobs {
		require.Zero(t, job.Attempts)
		delivery, decodeErr := outbox.DecodeFanoutDelivery(job.Payload)
		require.NoError(t, decodeErr)
		require.Equal(t, event.ID, delivery.Event.ID)
		require.Equal(
			t,
			outbox.FanoutDeliveryJobName(delivery.Target.Kind, event.Topic),
			job.Name,
		)
		deliveryIDs[delivery.ID] = struct{}{}
	}
	require.Len(t, deliveryIDs, 3)
}

func TestFanoutDeliveryIDAndDecoderRejectTampering(t *testing.T) {
	event := newFanoutEvent()
	target := outbox.FanoutTarget{
		Kind:     testFanoutWebhookKind,
		ID:       testSubscriptionA,
		Snapshot: json.RawMessage(`{"revision":1}`),
	}
	delivery := outbox.FanoutDelivery{
		ID:     outbox.FanoutDeliveryID(event.ID, target.Kind, target.ID),
		Event:  event,
		Target: target,
	}
	payload, err := json.Marshal(delivery)
	require.NoError(t, err)

	decoded, err := outbox.DecodeFanoutDelivery(string(payload))
	require.NoError(t, err)
	require.Equal(t, delivery.ID, decoded.ID)

	delivery.Target.ID = "another-subscription"
	payload, err = json.Marshal(delivery)
	require.NoError(t, err)
	_, err = outbox.DecodeFanoutDelivery(string(payload))
	require.ErrorIs(t, err, outbox.ErrInvalidFanout)
}

func TestPutFanoutValidatesTargetSetAndConfiguration(t *testing.T) {
	repo := newCapabilityRepo()
	legacy := newCapabilityService(t, repo)
	_, err := legacy.PutFanout(
		context.Background(),
		newFanoutEvent(),
		nil,
		time.Now().UTC(),
	)
	require.ErrorIs(t, err, outbox.ErrFanoutRepositoryNotConfigured)

	configured, err := outbox.New(
		outbox.WithJobsRepo(repo),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithFanoutJobsRepo(repo),
		outbox.WithTransactor(repo),
	)
	require.NoError(t, err)
	require.NotNil(t, configured)

	svc := newFanoutService(t, repo)
	target := outbox.FanoutTarget{Kind: testFanoutWebhookKind, ID: "same"}
	_, err = svc.PutFanout(
		context.Background(),
		newFanoutEvent(),
		[]outbox.FanoutTarget{target, target},
		time.Now().UTC(),
	)
	require.ErrorIs(t, err, outbox.ErrInvalidFanout)

	longEvent := newFanoutEvent()
	longEvent.Topic = strings.Repeat("a", 220)
	_, err = svc.PutFanout(
		context.Background(),
		longEvent,
		[]outbox.FanoutTarget{{Kind: strings.Repeat("k", 40), ID: "target"}},
		time.Now().UTC(),
	)
	require.ErrorIs(t, err, outbox.ErrInvalidFanout)
}

func newFanoutEvent() outbox.FanoutEvent {
	return outbox.FanoutEvent{
		ID:            types.NewMessageID(),
		Topic:         "cms.entry.published",
		SchemaVersion: 1,
		Payload:       json.RawMessage(`{"entryId":"entry-1"}`),
		OccurredAt:    time.Now().UTC(),
	}
}

func newFanoutService(t *testing.T, repo *capabilityRepo) *outbox.Service {
	t.Helper()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(repo),
		outbox.WithFanoutJobsRepo(repo),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	return svc
}

var _ outbox.FanoutPutter = (*outbox.Service)(nil)
