// Command batch demonstrates true multi-item handling and durable completion.
// It creates a disposable schema and removes only that schema on exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/assurrussa/outbox/backends/pgsql/migrator"
	pgruntime "github.com/assurrussa/outbox/backends/pgsql/runtime"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

const demoJobs = 6

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := run(ctx, os.Getenv("OUTBOX_PG_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("durably completed %d jobs; largest handler call=%d; active=%d failed=%d; removed schema %s\n",
		len(report.IDs), report.MaxBatch, report.Active, report.Failed, report.Schema)
}

type demoReport struct {
	Schema         string
	IDs            []outbox.JobID
	MaxBatch       int
	Active, Failed int
}

// This all-success handler acknowledges each input by its stable JobID.
// Real side effects still need idempotency: delivery can be repeated after a crash.
type batchJob struct {
	mu       sync.Mutex
	seen     map[outbox.JobID]bool
	maxBatch int
}

func (*batchJob) Name() string                    { return "batch_demo" }
func (*batchJob) ExecutionTimeout() time.Duration { return 2 * time.Second }
func (*batchJob) MaxAttempts() int                { return 3 }

func (j *batchJob) HandleBatch(ctx context.Context, items []outbox.BatchJobItem) (outbox.BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return outbox.BatchResult{}, err
	}
	result := outbox.BatchResult{Items: make([]outbox.BatchItemResult, 0, len(items))}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return outbox.BatchResult{}, err
		}
		result.Items = append(result.Items, outbox.BatchItemResult{JobID: item.JobID})
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.seen == nil {
		j.seen = make(map[outbox.JobID]bool)
	}
	for _, item := range items {
		j.seen[item.JobID] = true
	}
	if len(items) > j.maxBatch {
		j.maxBatch = len(items)
	}
	fmt.Printf("HandleBatch received %d items\n", len(items))
	return result, nil
}

func (j *batchJob) verify(ids []outbox.JobID) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.maxBatch < 2 {
		return j.maxBatch, errors.New("no multi-item handler invocation observed")
	}
	if len(j.seen) != len(ids) {
		return j.maxBatch, errors.New("handler did not account for every staged JobID")
	}
	for _, id := range ids {
		if !j.seen[id] {
			return j.maxBatch, fmt.Errorf("handler did not see job %s", id)
		}
	}
	return j.maxBatch, nil
}

func run(ctx context.Context, dsn string) (report demoReport, err error) {
	runtime, cleanup, err := openDemo(ctx, dsn)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	if err = runtime.Client().DB().Pool().QueryRow(ctx, "SELECT current_schema()").Scan(&report.Schema); err != nil {
		return report, err
	}
	job := &batchJob{}
	if err = runtime.Service().RegisterBatchJob(job, outbox.BatchConfig{
		MaxMessages: demoJobs, MaxBytes: 4096, MaxWait: 25 * time.Millisecond,
	}); err != nil {
		return report, err
	}
	// Stage every ready item before starting the worker, so a true batch is available.
	items := make([]outbox.UniqueBatchPut, demoJobs)
	now := time.Now().UTC()
	for i := range items {
		items[i] = outbox.UniqueBatchPut{
			DeduplicationKey: fmt.Sprintf("demo-%d", i), Name: job.Name(),
			SchemaVersion: outbox.DefaultSchemaVersion,
			Payload:       fmt.Sprintf("message %d", i), AvailableAt: now,
		}
	}
	staged, err := runtime.Service().PutVersionedUniqueBatch(ctx, items)
	if err != nil {
		return report, err
	}
	for _, item := range staged {
		report.IDs = append(report.IDs, item.JobID)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runtime.Run(runCtx) }()
	// Join the worker before closing its pool or dropping the owned schema.
	defer func() { err = errors.Join(err, stopWorker(stop, done)) }()
	err = waitForCompletion(ctx, func(ctx context.Context) (bool, error) {
		// One database snapshot observes committed removals and failed records.
		// A handler callback alone is not proof that its acknowledgement committed.
		queryErr := runtime.Client().DB().Pool().QueryRow(ctx,
			"SELECT (SELECT count(*) FROM jobs), (SELECT count(*) FROM jobs_failed)",
		).Scan(&report.Active, &report.Failed)
		if queryErr != nil {
			return false, queryErr
		}
		if report.Failed != 0 {
			return false, fmt.Errorf("unexpected failed jobs: %d", report.Failed)
		}
		return report.Active == 0, nil
	})
	if err != nil {
		return report, err
	}
	report.MaxBatch, err = job.verify(report.IDs)
	return report, err
}

func waitForCompletion(ctx context.Context, check func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		complete, err := check(ctx)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if complete {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func stopWorker(stop context.CancelFunc, done <-chan error) error {
	stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-timer.C:
		return errors.New("worker did not stop within 5 seconds")
	}
}

func openDemo(ctx context.Context, dsn string) (*pgruntime.Runtime, func() error, error) {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return nil, nil, fmt.Errorf("parse OUTBOX_PG_DSN: %w", err)
	}
	if (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return nil, nil, errors.New("OUTBOX_PG_DSN must be a PostgreSQL URL for a disposable development database")
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	schema := "outbox_batch_" + strings.ReplaceAll(outbox.NewJobID().String(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		_ = admin.Close(ctx)
		return nil, nil, err
	}
	var runtime *pgruntime.Runtime
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var closeErr error
		if runtime != nil {
			closeErr = runtime.Close()
		}
		_, dropErr := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE")
		return errors.Join(closeErr, dropErr, admin.Close(cleanupCtx))
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	runtime, err = pgruntime.Open(ctx, pgruntime.Config{DSN: parsed.String(), Workers: 1, IdleTime: 100 * time.Millisecond, ReserveFor: 5 * time.Second, MinConnectionsCount: 1, MaxConnectionsCount: 2})
	if err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	sqlDB := stdlib.OpenDBFromPool(runtime.Client().DB().Pool())
	err = migrator.RunEmbedded(ctx, sqlDB, logger.Default(), migrator.WithCommand("up"))
	err = errors.Join(err, sqlDB.Close())
	if err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	return runtime, cleanup, nil
}
