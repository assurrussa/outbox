package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

func TestBatchLeaseManagerInheritsRunCancellation(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(t.Context())
	batchCtx, cancelBatch := context.WithCancelCause(runCtx)
	manager := newBatchLeaseManager(
		batchCtx,
		nil,
		nil,
		LeaseToken{},
		time.Hour,
		cancelBatch,
	)
	t.Cleanup(func() {
		manager.stop()
		<-manager.done
	})

	cancelRun()
	select {
	case <-manager.done:
	case <-time.After(time.Second):
		t.Fatal("batch lease heartbeat kept running after Run context cancellation")
	}
}

func TestBatchStartError(t *testing.T) {
	t.Run("active batch", func(t *testing.T) {
		if err := batchStartError(t.Context(), make(chan struct{})); err != nil {
			t.Fatalf("active batch start error: %v", err)
		}
	})

	t.Run("batch cancellation has priority over drain", func(t *testing.T) {
		batchCtx, cancelBatch := context.WithCancelCause(t.Context())
		cancelBatch(ErrLeaseLost)
		drain := make(chan struct{})
		close(drain)

		err := batchStartError(batchCtx, drain)
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("batch start error = %v, want %v", err, ErrLeaseLost)
		}
	})

	t.Run("drain", func(t *testing.T) {
		drain := make(chan struct{})
		close(drain)

		err := batchStartError(t.Context(), drain)
		if !errors.Is(err, ErrServiceDraining) {
			t.Fatalf("batch start error = %v, want %v", err, ErrServiceDraining)
		}
	})
}

func TestProcessBatchJobRejectsCancelledAdmission(t *testing.T) {
	job := models.Job{
		ID:            types.NewJobID(),
		Name:          "batch",
		SchemaVersion: DefaultSchemaVersion,
	}
	handler := &batchAdmissionJob{name: job.Name}
	service := &Service{
		jobs: map[JobCapability]Job{
			{Name: job.Name, SchemaVersion: job.SchemaVersion}: handler,
		},
		drain: make(chan struct{}),
	}
	batchCtx, cancelBatch := context.WithCancelCause(t.Context())
	cancelBatch(ErrLeaseLost)

	started, err := service.processBatchJob(
		batchCtx,
		logger.Discard(),
		&batchLeaseManager{},
		job,
	)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("process batch job error = %v, want %v", err, ErrLeaseLost)
	}
	if started {
		t.Fatal("cancelled batch job reported as started")
	}
	if handler.called {
		t.Fatal("cancelled batch job entered handler")
	}
}

type batchAdmissionJob struct {
	name   string
	called bool
}

func (j *batchAdmissionJob) Name() string { return j.name }

func (j *batchAdmissionJob) Handle(context.Context, string) error {
	j.called = true

	return nil
}

func (*batchAdmissionJob) ExecutionTimeout() time.Duration { return time.Second }

func (*batchAdmissionJob) MaxAttempts() int { return 1 }
