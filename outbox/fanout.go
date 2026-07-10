package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assurrussa/outbox/shared/types"
)

const (
	FanoutDispatcherJobName       = "outbox.fanout.dispatch"
	FanoutDispatcherSchemaVersion = SchemaVersion(1)

	fanoutDeliveryNamePrefix = "fanout."
	fanoutDispatcherAttempts = 10
	fanoutDispatcherTimeout  = 30 * time.Second
)

var (
	ErrFanoutRepositoryNotConfigured = errors.New("outbox fan-out repository is not configured")
	ErrIdempotencyConflict           = errors.New("outbox idempotency key conflicts with different job content")
	ErrInvalidFanout                 = errors.New("outbox invalid fan-out snapshot")
)

// FanoutEvent is the immutable integration event copied into every delivery.
type FanoutEvent struct {
	ID            types.MessageID `json:"id"`
	Topic         string          `json:"topic"`
	SchemaVersion SchemaVersion   `json:"schemaVersion"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurredAt"`
}

// FanoutTarget identifies one independently retried consumer. Snapshot stores
// immutable consumer configuration such as a webhook config and secret revision.
// It must contain references/revisions, never plaintext secret material.
type FanoutTarget struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Snapshot json.RawMessage `json:"snapshot"`
}

// FanoutDelivery is the stable payload handled by a target-specific worker.
type FanoutDelivery struct {
	ID     types.MessageID `json:"id"`
	Event  FanoutEvent     `json:"event"`
	Target FanoutTarget    `json:"target"`
}

type fanoutSnapshot struct {
	Event       FanoutEvent    `json:"event"`
	Targets     []FanoutTarget `json:"targets"`
	AvailableAt time.Time      `json:"availableAt"`
}

// FanoutDeliveryJobName returns the capability name for a consumer kind and topic.
func FanoutDeliveryJobName(kind, topic string) string {
	return fanoutDeliveryNamePrefix + kind + "." + topic
}

// FanoutDeliveryID deterministically identifies one event/consumer delivery.
func FanoutDeliveryID(eventID types.MessageID, kind, targetID string) types.MessageID {
	namespace := uuid.NewSHA1(uuid.NameSpaceOID, []byte("github.com/assurrussa/outbox/fanout"))
	name := eventID.String() + "\x00" + kind + "\x00" + targetID

	return types.MessageID(uuid.NewSHA1(namespace, []byte(name)))
}

// DecodeFanoutDelivery validates a delivery payload before a consumer uses it.
func DecodeFanoutDelivery(payload string) (FanoutDelivery, error) {
	var delivery FanoutDelivery
	if err := json.Unmarshal([]byte(payload), &delivery); err != nil {
		return FanoutDelivery{}, fmt.Errorf("%w: decode delivery: %w", ErrInvalidFanout, err)
	}

	event, err := normalizeFanoutEvent(delivery.Event)
	if err != nil {
		return FanoutDelivery{}, err
	}
	target, err := normalizeFanoutTarget(delivery.Target)
	if err != nil {
		return FanoutDelivery{}, err
	}

	expectedID := FanoutDeliveryID(event.ID, target.Kind, target.ID)
	if delivery.ID != expectedID {
		return FanoutDelivery{}, fmt.Errorf("%w: delivery id does not match event and target", ErrInvalidFanout)
	}

	delivery.Event = event
	delivery.Target = target

	return delivery, nil
}

// PutFanout stores one immutable fan-out snapshot. The target set is sorted and
// keyed by event ID so retries cannot silently change eligibility.
func (s *Service) PutFanout(
	ctx context.Context,
	event FanoutEvent,
	targets []FanoutTarget,
	availableAt time.Time,
) (types.JobID, error) {
	if s.fanoutJobsRepo == nil {
		return types.JobIDNil, ErrFanoutRepositoryNotConfigured
	}

	snapshot, err := normalizeFanoutSnapshot(fanoutSnapshot{
		Event:       event,
		Targets:     targets,
		AvailableAt: availableAt,
	})
	if err != nil {
		return types.JobIDNil, err
	}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		return types.JobIDNil, fmt.Errorf("encode fan-out snapshot: %w", err)
	}

	jobID, err := s.fanoutJobsRepo.CreateJobVersionedUnique(
		ctx,
		fanoutEventDeduplicationKey(snapshot.Event.ID),
		FanoutDispatcherJobName,
		FanoutDispatcherSchemaVersion,
		string(payload),
		snapshot.AvailableAt,
	)
	if err != nil {
		return types.JobIDNil, fmt.Errorf("create fan-out snapshot: %w", err)
	}

	return jobID, nil
}

func normalizeFanoutSnapshot(snapshot fanoutSnapshot) (fanoutSnapshot, error) {
	event, err := normalizeFanoutEvent(snapshot.Event)
	if err != nil {
		return fanoutSnapshot{}, err
	}

	targets := make([]FanoutTarget, 0, len(snapshot.Targets))
	seen := make(map[string]struct{}, len(snapshot.Targets))
	for _, item := range snapshot.Targets {
		target, targetErr := normalizeFanoutTarget(item)
		if targetErr != nil {
			return fanoutSnapshot{}, targetErr
		}
		if len(FanoutDeliveryJobName(target.Kind, event.Topic)) > 255 {
			return fanoutSnapshot{}, fmt.Errorf(
				"%w: delivery capability name is too long for target %q/%q",
				ErrInvalidFanout,
				target.Kind,
				target.ID,
			)
		}

		key := target.Kind + "\x00" + target.ID
		if _, ok := seen[key]; ok {
			return fanoutSnapshot{}, fmt.Errorf(
				"%w: duplicate target %q/%q",
				ErrInvalidFanout,
				target.Kind,
				target.ID,
			)
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Kind == targets[j].Kind {
			return targets[i].ID < targets[j].ID
		}

		return targets[i].Kind < targets[j].Kind
	})

	return fanoutSnapshot{
		Event:       event,
		Targets:     targets,
		AvailableAt: snapshot.AvailableAt.UTC(),
	}, nil
}

func normalizeFanoutEvent(event FanoutEvent) (FanoutEvent, error) {
	if err := event.ID.Validate(); err != nil {
		return FanoutEvent{}, fmt.Errorf("%w: event id: %w", ErrInvalidFanout, err)
	}
	if !validFanoutToken(event.Topic, true) {
		return FanoutEvent{}, fmt.Errorf("%w: invalid topic %q", ErrInvalidFanout, event.Topic)
	}
	if len(FanoutDeliveryJobName("consumer", event.Topic)) > 255 {
		return FanoutEvent{}, fmt.Errorf("%w: topic is too long", ErrInvalidFanout)
	}
	if event.SchemaVersion <= 0 {
		return FanoutEvent{}, fmt.Errorf("%w: schema version must be positive", ErrInvalidFanout)
	}
	if event.OccurredAt.IsZero() {
		return FanoutEvent{}, fmt.Errorf("%w: occurredAt is required", ErrInvalidFanout)
	}
	if !json.Valid(event.Payload) {
		return FanoutEvent{}, fmt.Errorf("%w: event payload must be valid JSON", ErrInvalidFanout)
	}

	event.Payload = append(json.RawMessage(nil), event.Payload...)
	event.OccurredAt = event.OccurredAt.UTC()

	return event, nil
}

func normalizeFanoutTarget(target FanoutTarget) (FanoutTarget, error) {
	if !validFanoutToken(target.Kind, false) {
		return FanoutTarget{}, fmt.Errorf("%w: invalid target kind %q", ErrInvalidFanout, target.Kind)
	}
	if strings.TrimSpace(target.ID) == "" || len(target.ID) > 512 {
		return FanoutTarget{}, fmt.Errorf("%w: invalid target id", ErrInvalidFanout)
	}
	if len(FanoutDeliveryJobName(target.Kind, "topic")) > 255 {
		return FanoutTarget{}, fmt.Errorf("%w: target kind is too long", ErrInvalidFanout)
	}

	if len(target.Snapshot) == 0 {
		target.Snapshot = json.RawMessage(`{}`)
	}
	if !json.Valid(target.Snapshot) {
		return FanoutTarget{}, fmt.Errorf("%w: target snapshot must be valid JSON", ErrInvalidFanout)
	}
	target.Snapshot = append(json.RawMessage(nil), target.Snapshot...)

	return target, nil
}

func validFanoutToken(value string, allowDot bool) bool {
	if value == "" {
		return false
	}

	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		if char == '-' || char == '_' || allowDot && char == '.' {
			continue
		}

		return false
	}

	return true
}

func fanoutEventDeduplicationKey(eventID types.MessageID) string {
	return "fanout-event:" + eventID.String()
}

func fanoutDeliveryDeduplicationKey(deliveryID types.MessageID) string {
	return "fanout-delivery:" + deliveryID.String()
}

type fanoutDispatcher struct {
	repo       FanoutJobsRepository
	transactor Transactor
}

func (fanoutDispatcher) Name() string { return FanoutDispatcherJobName }

func (fanoutDispatcher) SchemaVersion() SchemaVersion { return FanoutDispatcherSchemaVersion }

func (fanoutDispatcher) ExecutionTimeout() time.Duration { return fanoutDispatcherTimeout }

func (fanoutDispatcher) MaxAttempts() int { return fanoutDispatcherAttempts }

func (d fanoutDispatcher) Handle(ctx context.Context, payload string) error {
	var snapshot fanoutSnapshot
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return fmt.Errorf("decode fan-out snapshot: %w", err)
	}

	snapshot, err := normalizeFanoutSnapshot(snapshot)
	if err != nil {
		return err
	}

	return d.transactor.RunInTx(ctx, func(txCtx context.Context) error {
		for _, target := range snapshot.Targets {
			delivery := FanoutDelivery{
				ID:     FanoutDeliveryID(snapshot.Event.ID, target.Kind, target.ID),
				Event:  snapshot.Event,
				Target: target,
			}

			deliveryPayload, marshalErr := json.Marshal(delivery)
			if marshalErr != nil {
				return fmt.Errorf("encode fan-out delivery: %w", marshalErr)
			}

			_, createErr := d.repo.CreateJobVersionedUnique(
				txCtx,
				fanoutDeliveryDeduplicationKey(delivery.ID),
				FanoutDeliveryJobName(target.Kind, snapshot.Event.Topic),
				snapshot.Event.SchemaVersion,
				string(deliveryPayload),
				snapshot.AvailableAt,
			)
			if createErr != nil {
				return fmt.Errorf("create fan-out delivery %s: %w", delivery.ID, createErr)
			}
		}

		return nil
	})
}
