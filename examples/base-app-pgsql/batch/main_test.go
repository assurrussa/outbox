package main

import (
	"context"
	"errors"
	"testing"

	"github.com/assurrussa/outbox/outbox"
)

func TestBatchResultsCoverEveryJobID(t *testing.T) {
	items := []outbox.BatchJobItem{
		{JobID: outbox.NewJobID(), Payload: "first"},
		{JobID: outbox.NewJobID(), Payload: "second"},
		{JobID: outbox.NewJobID(), Payload: "third"},
	}
	job := &batchJob{}
	result, err := job.HandleBatch(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[outbox.JobID]bool)
	for _, item := range items {
		want[item.JobID] = true
	}
	if len(result.Items) != len(items) {
		t.Fatalf("got %d results", len(result.Items))
	}
	for _, item := range result.Items {
		if !want[item.JobID] || item.Err != nil {
			t.Fatalf("invalid result: %+v", item)
		}
		delete(want, item.JobID)
	}
	if len(want) != 0 {
		t.Fatalf("unacknowledged IDs: %v", want)
	}
	maxBatch, err := job.verify([]outbox.JobID{items[2].JobID, items[0].JobID, items[1].JobID})
	if err != nil || maxBatch != len(items) {
		t.Fatalf("observation: batch=%d err=%v", maxBatch, err)
	}
}

func TestCanceledHandlerDoesNotAcknowledge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job := &batchJob{}
	result, err := job.HandleBatch(ctx, []outbox.BatchJobItem{{JobID: outbox.NewJobID()}})
	if !errors.Is(err, context.Canceled) || len(result.Items) != 0 || len(job.seen) != 0 {
		t.Fatalf("cancellation falsely acknowledged: result=%+v err=%v", result, err)
	}
}

func TestCompletionRequiresDurableCheck(t *testing.T) {
	calls := 0
	err := waitForCompletion(context.Background(), func(context.Context) (bool, error) {
		calls++
		return calls == 2, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestCancellationDoesNotBecomeCompletion(t *testing.T) {
	for _, cancelDuringCheck := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !cancelDuringCheck {
			cancel()
		}
		err := waitForCompletion(ctx, func(context.Context) (bool, error) {
			cancel()
			return true, nil
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation falsely succeeded: %v", err)
		}
	}
}

func TestCompletionPropagatesQueryError(t *testing.T) {
	want := errors.New("database unavailable")
	err := waitForCompletion(context.Background(), func(context.Context) (bool, error) { return false, want })
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestStopWorkerPropagatesFailure(t *testing.T) {
	done := make(chan error, 1)
	want := errors.New("worker failed")
	done <- want
	stopped := false
	err := stopWorker(func() { stopped = true }, done)
	if !stopped || !errors.Is(err, want) {
		t.Fatalf("stopped=%t err=%v", stopped, err)
	}
}
