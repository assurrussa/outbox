package outbox

import (
	"context"
	"testing"
	"time"
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
