// Command transactional proves that a business write and Put share one commit
// or rollback. It uses a fresh schema and removes only that schema on exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/assurrussa/outbox/backends/pgsql/migrator"
	pgruntime "github.com/assurrussa/outbox/backends/pgsql/runtime"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

var errDemoRollback = errors.New("demonstrate rollback after Put")

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, os.Getenv("OUTBOX_PG_DSN")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn string) (err error) {
	runtime, cleanup, err := openDemo(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	committed, err := writeOrder(ctx, runtime, "committed", "order.created", nil)
	if err != nil {
		return err
	}
	if err := assertCounts(ctx, runtime, 1, 1); err != nil {
		return err
	}
	rolledBack, err := writeOrder(ctx, runtime, "rolled-back", "order.created", errDemoRollback)
	if !errors.Is(err, errDemoRollback) || !rolledBack.IsZero() {
		return fmt.Errorf("expected rollback, got ID %s: %w", rolledBack, err)
	}
	if err := assertCounts(ctx, runtime, 1, 1); err != nil {
		return err
	}
	fmt.Printf("committed business row and job %s; rollback persisted neither row nor job\n", committed)
	return nil
}

// The demo calls writeOrder without a surrounding transaction, so successful
// RunInTx confirms commit. Nested calls reuse the existing transaction without
// committing; their errors must propagate to the outer callback. Always pass
// txCtx to Put; passing the outer ctx would enqueue independently.
func writeOrder(ctx context.Context, runtime *pgruntime.Runtime, orderID, jobName string, abort error) (outbox.JobID, error) {
	var jobID outbox.JobID
	err := runtime.Transactor().RunInTx(ctx, func(txCtx context.Context) error {
		tx := storage.GetTx(txCtx)
		if tx == nil {
			return errors.New("missing pgx transaction")
		}
		if _, err := tx.Exec(txCtx, "INSERT INTO orders (id) VALUES ($1)", orderID); err != nil {
			return err
		}
		var err error
		jobID, err = runtime.Service().Put(txCtx, jobName, orderID, time.Now().UTC())
		if err != nil {
			return err
		}
		return abort
	})
	if err != nil {
		return outbox.JobID{}, err
	}
	return jobID, nil
}

func assertCounts(ctx context.Context, runtime *pgruntime.Runtime, orders, jobs int) error {
	var gotOrders, gotJobs int
	err := runtime.Client().DB().Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM orders), (SELECT count(*) FROM jobs)").Scan(&gotOrders, &gotJobs)
	if err != nil {
		return err
	}
	if gotOrders != orders || gotJobs != jobs {
		return fmt.Errorf("counts: orders=%d jobs=%d, want %d/%d", gotOrders, gotJobs, orders, jobs)
	}
	return nil
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
	schema := "outbox_quickstart_" + strings.ReplaceAll(outbox.NewJobID().String(), "-", "")
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
	runtime, err = pgruntime.Open(ctx, pgruntime.Config{DSN: parsed.String(), MinConnectionsCount: 1, MaxConnectionsCount: 2})
	if err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	sqlDB := stdlib.OpenDBFromPool(runtime.Client().DB().Pool())
	err = migrator.RunEmbedded(ctx, sqlDB, logger.Default(), migrator.WithCommand("up"))
	err = errors.Join(err, sqlDB.Close())
	if err == nil {
		_, err = runtime.Client().DB().Pool().Exec(ctx, "CREATE TABLE orders (id text PRIMARY KEY)")
	}
	if err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	return runtime, cleanup, nil
}
