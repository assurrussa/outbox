package outbox

import (
	"errors"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
)

// RuntimeOutcomeKind identifies a confirmed storage result or unusable lease.
type RuntimeOutcomeKind string

const (
	OutcomeACKConfirmed   RuntimeOutcomeKind = "ack_confirmed"
	OutcomeRetryPersisted RuntimeOutcomeKind = "retry_persisted"
	OutcomeDeferPersisted RuntimeOutcomeKind = "defer_persisted"
	OutcomeDLQCommitted   RuntimeOutcomeKind = "dlq_committed"
	OutcomeLeaseLost      RuntimeOutcomeKind = "lease_lost"
)

// RuntimeOutcome contains no payload, lease token, error text or context values.
// Per-job outcomes identify the admitted one-based Attempt; defer compensates
// that attempt in storage. LeaseLost is a group-level signal: JobID, Capability,
// Attempt and AvailableAt are zero, and ClaimedJobs counts known group claims.
// It means usable ownership was lost, not that every row was individually stolen.
type RuntimeOutcome struct {
	Kind        RuntimeOutcomeKind
	JobID       JobID
	Capability  JobCapability
	Attempt     int
	AvailableAt time.Time
	ObservedAt  time.Time
	ClaimedJobs int
}

// Observer is a best-effort, nonblocking telemetry sink. Callers own buffering
// and consumption. Nil disables observation; full or closed sinks drop events.
// Keep the sink open during Run and close only after Run joins all workers.
// The runtime executes no consumer code; telemetry errors/panics are caller-owned.
// Notifications are neither durable nor an exactly-once audit stream.
type Observer chan<- RuntimeOutcome

// WithObserver configures optional persisted-outcome telemetry. It does not
// alter delivery, lease, transaction or worker-error behavior.
func WithObserver(observer Observer) OptOptionsSetter {
	return func(o *Options) { o.observer = observer }
}

func (s *Service) emitOutcome(event RuntimeOutcome) {
	if s.observer == nil {
		return
	}
	// A sink closed before use must not turn confirmed storage into a worker
	// failure. Callers must not race channel close with active Run sends.
	defer func() { _ = recover() }()
	select {
	case s.observer <- event:
	default:
	}
}

func (s *Service) observeJobOutcome(job models.Job, kind RuntimeOutcomeKind, availableAt time.Time) {
	if s.observer == nil {
		return
	}
	if kind == OutcomeDLQCommitted && !s.atomicDLQ {
		return
	}
	s.emitOutcome(RuntimeOutcome{
		Kind: kind, JobID: job.ID,
		Capability: JobCapability{Name: job.Name, SchemaVersion: normalizeSchemaVersion(job.SchemaVersion)},
		Attempt:    job.Attempts, AvailableAt: availableAt.UTC(), ObservedAt: time.Now().UTC(),
	})
}

func (s *Service) observeLeaseLoss(err error, claimedJobs int) {
	if s.observer == nil || !errors.Is(err, ErrLeaseLost) {
		return
	}
	s.emitOutcome(RuntimeOutcome{Kind: OutcomeLeaseLost, ClaimedJobs: claimedJobs, ObservedAt: time.Now().UTC()})
}

func (s *Service) observeBatchOutcomes(jobs []models.Job, outcomes []BatchJobOutcome) {
	if s.observer == nil {
		return
	}
	jobsByID := make(map[JobID]models.Job, len(jobs))
	for _, job := range jobs {
		jobsByID[job.ID] = job
	}
	for _, outcome := range outcomes {
		job, ok := jobsByID[outcome.JobID]
		if !ok {
			continue
		}
		switch outcome.Kind {
		case BatchJobOutcomeSuccess:
			s.observeJobOutcome(job, OutcomeACKConfirmed, time.Time{})
		case BatchJobOutcomeRetry:
			s.observeJobOutcome(job, OutcomeRetryPersisted, outcome.AvailableAt)
		case BatchJobOutcomeDefer:
			s.observeJobOutcome(job, OutcomeDeferPersisted, outcome.AvailableAt)
		case BatchJobOutcomeDLQ:
			s.observeJobOutcome(job, OutcomeDLQCommitted, time.Time{})
		}
	}
}
