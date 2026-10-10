//nolint:testpackage // The benchmark isolates the internal dispatcher from storage and workers.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const fanoutBenchmarkPayloadBytes = 1024

// BenchmarkFanoutPlanning measures synthetic core preparation, not durable delivery throughput.
// Each operation uses one event and a fixed target set. The sink retains only its last payload.
func BenchmarkFanoutPlanning(b *testing.B) {
	for _, count := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprintf("targets=%d", count), func(b *testing.B) {
			event, targets := fanoutBenchmarkInput(count)
			repo := &fanoutBenchmarkRepo{jobID: JobID(event.ID)}
			service := &Service{Options: Options{fanoutJobsRepo: repo}}
			ctx := context.Background()
			_, err := service.PutFanout(ctx, event, targets, event.OccurredAt)
			if err != nil {
				b.Fatal(err)
			}
			snapshot := repo.lastPayload
			dispatcher := fanoutDispatcher{repo: repo, transactor: &executionBatchTestTransactor{}}
			verifyFanoutBenchmark(b, dispatcher, repo, snapshot, event, targets)

			b.Run("enqueue", func(b *testing.B) {
				repo.reset()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, putErr := service.PutFanout(ctx, event, targets, event.OccurredAt); putErr != nil {
						b.Fatal(putErr)
					}
				}
				b.StopTimer()
				if repo.calls != b.N {
					b.Fatalf("enqueue calls = %d, want %d", repo.calls, b.N)
				}
				reportFanoutBenchmark(b, count, repo)
			})

			b.Run("dispatch", func(b *testing.B) {
				repo.reset()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if dispatchErr := dispatcher.Handle(ctx, snapshot); dispatchErr != nil {
						b.Fatal(dispatchErr)
					}
				}
				b.StopTimer()
				if repo.calls != b.N*count {
					b.Fatalf("delivery calls = %d, want %d", repo.calls, b.N*count)
				}
				reportFanoutBenchmark(b, count, repo)
			})
		})
	}
}

func fanoutBenchmarkInput(count int) (FanoutEvent, []FanoutTarget) {
	event := FanoutEvent{
		ID:            MessageID(uuid.MustParse("00000000-0000-4000-8000-000000000001")),
		Topic:         "baseline.event",
		SchemaVersion: 1,
		Payload:       json.RawMessage(`{"data":"` + strings.Repeat("x", fanoutBenchmarkPayloadBytes-11) + `"}`),
		OccurredAt:    time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC),
	}
	targets := make([]FanoutTarget, count)
	for index := range targets {
		// Reverse order exercises canonical sorting; IDs keep the same byte length.
		targets[index] = FanoutTarget{
			Kind:     "webhook",
			ID:       fmt.Sprintf("target-%05d", count-index-1),
			Snapshot: json.RawMessage(`{"revision":1}`),
		}
	}
	return event, targets
}

func verifyFanoutBenchmark(
	b *testing.B,
	dispatcher fanoutDispatcher,
	repo *fanoutBenchmarkRepo,
	snapshot string,
	event FanoutEvent,
	targets []FanoutTarget,
) {
	b.Helper()
	if len(event.Payload) != fanoutBenchmarkPayloadBytes {
		b.Fatal("unexpected event payload size")
	}
	repo.reset()
	repo.verify = func(key, name string, version SchemaVersion, payload string, availableAt time.Time) {
		b.Helper()
		if repo.calls < 1 || repo.calls > len(targets) {
			b.Fatalf("unexpected delivery call %d for %d targets", repo.calls, len(targets))
		}
		delivery, err := DecodeFanoutDelivery(payload)
		if err != nil {
			b.Fatal(err)
		}
		target := targets[len(targets)-repo.calls]
		if delivery.Target.ID != target.ID || delivery.Target.Kind != target.Kind ||
			string(delivery.Target.Snapshot) != string(target.Snapshot) ||
			delivery.Event.ID != event.ID || delivery.Event.Topic != event.Topic ||
			delivery.Event.SchemaVersion != event.SchemaVersion ||
			string(delivery.Event.Payload) != string(event.Payload) ||
			!delivery.Event.OccurredAt.Equal(event.OccurredAt) ||
			key != fanoutDeliveryDeduplicationKey(delivery.ID) ||
			name != FanoutDeliveryJobName(target.Kind, event.Topic) ||
			version != event.SchemaVersion || !availableAt.Equal(event.OccurredAt) {
			b.Fatalf("unexpected delivery at index %d", repo.calls-1)
		}
	}
	if err := dispatcher.Handle(context.Background(), snapshot); err != nil {
		b.Fatal(err)
	}
	if repo.calls != len(targets) {
		b.Fatalf("preflight deliveries = %d, want %d", repo.calls, len(targets))
	}
	repo.verify = nil
	repo.reset()
}

func reportFanoutBenchmark(b *testing.B, count int, repo *fanoutBenchmarkRepo) {
	b.Helper()
	b.ReportMetric(float64(count), "targets/op")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(count), "ns/target")
	b.ReportMetric(float64(repo.calls)/float64(b.N), "repo-calls/op")
	b.ReportMetric(float64(repo.payloadBytes)/float64(b.N), "encoded-B/op")
}

// fanoutBenchmarkRepo is a counting sink, not an idempotency or database implementation.
// It deliberately does not retain every delivery, allocate IDs, or simulate I/O.
type fanoutBenchmarkRepo struct {
	jobID        JobID
	calls        int
	payloadBytes int64
	lastPayload  string
	verify       func(string, string, SchemaVersion, string, time.Time)
}

func (r *fanoutBenchmarkRepo) reset() {
	r.calls = 0
	r.payloadBytes = 0
	r.lastPayload = ""
}

func (r *fanoutBenchmarkRepo) CreateJobVersionedUnique(
	_ context.Context,
	key, name string,
	version SchemaVersion,
	payload string,
	availableAt time.Time,
) (JobID, error) {
	r.calls++
	r.payloadBytes += int64(len(payload))
	r.lastPayload = payload
	if r.verify != nil {
		r.verify(key, name, version, payload, availableAt)
	}
	return r.jobID, nil
}
