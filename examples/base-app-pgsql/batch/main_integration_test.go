//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/assurrussa/outbox/outbox"
)

func TestTrueBatchDurableCompletionAndIsolation(t *testing.T) {
	dsn := os.Getenv("OUTBOX_PG_DSN")
	if dsn == "" {
		t.Skip("set OUTBOX_PG_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	sentinel := "batch_sentinel_" + strings.ReplaceAll(outbox.NewJobID().String(), "-", "")
	quoted := pgx.Identifier{sentinel}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	if _, err := admin.Exec(ctx, "CREATE TABLE "+quoted+".sentinel (value text); INSERT INTO "+quoted+".sentinel VALUES ('untouched')"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	report, err := run(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("unbounded exit: %s", elapsed)
	}
	if report.MaxBatch < 2 || len(report.IDs) != demoJobs || report.Active != 0 || report.Failed != 0 {
		t.Fatalf("incomplete batch: %+v", report)
	}
	seen := make(map[outbox.JobID]bool)
	for _, id := range report.IDs {
		if id.IsZero() || seen[id] {
			t.Fatalf("invalid staged ID %s", id)
		}
		seen[id] = true
	}
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)", report.Schema).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if report.Schema == "" || exists {
		t.Fatalf("owned schema not removed: %q", report.Schema)
	}
	var value string
	if err := admin.QueryRow(ctx, "SELECT value FROM "+quoted+".sentinel").Scan(&value); err != nil || value != "untouched" {
		t.Fatalf("unrelated sentinel changed: value=%q err=%v", value, err)
	}
}
