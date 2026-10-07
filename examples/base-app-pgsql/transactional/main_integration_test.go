//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestBusinessWriteAndPutAtomicity(t *testing.T) {
	dsn := os.Getenv("OUTBOX_PG_DSN")
	if dsn == "" {
		t.Skip("set OUTBOX_PG_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime, cleanup, err := openDemo(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	id, err := writeOrder(ctx, runtime, "committed", "order.created", nil)
	if err != nil || id.IsZero() {
		t.Fatalf("commit: id=%s err=%v", id, err)
	}
	if err := assertCounts(ctx, runtime, 1, 1); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := runtime.Client().DB().Pool().QueryRow(ctx, "SELECT payload FROM jobs WHERE id=$1::uuid", id.String()).Scan(&payload); err != nil || payload != "committed" {
		t.Fatalf("committed job: payload=%q err=%v", payload, err)
	}
	for _, scenario := range []struct {
		name, jobName string
		abort         error
	}{
		{"callback rollback", "order.created", errDemoRollback},
		{"Put validation failure", "", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			id, err := writeOrder(ctx, runtime, scenario.name, scenario.jobName, scenario.abort)
			if err == nil || !id.IsZero() {
				t.Fatalf("rollback: id=%s err=%v", id, err)
			}
			if scenario.abort != nil && !errors.Is(err, scenario.abort) {
				t.Fatalf("rollback cause lost: %v", err)
			}
			if err := assertCounts(ctx, runtime, 1, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}
