package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	pgsqlmigrator "github.com/assurrussa/outbox/backends/pgsql/migrator"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlinit"
	pgsqltx "github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	sharedjob "github.com/assurrussa/outbox/shared/job"
	"github.com/jackc/pgx/v5/stdlib"
)

func main() {
	ctx := context.Background()
	lg := outboxlogger.DefaultText().Named("base-app-pgsql")

	if err := run(ctx, lg); err != nil {
		lg.ErrorContext(ctx, "base-app-pgsql error", outboxlogger.Error(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, log outboxlogger.Logger) error {
	dsn := resolvePostgresDSN()
	client, err := pgsqlinit.Create(ctx, dsn, pgsqlclient.WithLogger(log))
	if err != nil {
		return fmt.Errorf("init pgsql: %w", err)
	}
	defer func() { _ = client.Close() }()
	log.InfoContext(ctx, "postgres connected")

	sqlDB := stdlib.OpenDBFromPool(client.DB().Pool())
	defer func() { _ = sqlDB.Close() }()
	if err := pgsqlmigrator.RunEmbedded(
		ctx,
		sqlDB,
		log,
		pgsqlmigrator.WithCommand("up"),
	); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := resetDemoData(ctx, sqlDB); err != nil {
		return fmt.Errorf("reset demo data: %w", err)
	}

	jobs := jobsrepo.Must(jobsrepo.NewOptions(client))
	failed := jobsfailedrepo.Must(jobsfailedrepo.NewOptions(client))
	trx := pgsqltx.New(client.DB())

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(200*time.Millisecond),
		outbox.WithReserveFor(5*time.Second),
		outbox.WithJobsRepo(jobs),
		outbox.WithJobsStatRepo(jobs),
		outbox.WithJobsFailedRepo(failed),
		outbox.WithTransactor(trx),
		outbox.WithLogger(log),
	)
	if err != nil {
		return fmt.Errorf("create outbox service: %w", err)
	}

	svc.MustRegisterJob(newPrintJob(log))

	if err := putDemoJobs(ctx, svc); err != nil {
		return err
	}

	if err := checkStats(ctx, log, svc, failed); err != nil {
		return fmt.Errorf("check stats: %w", err)
	}

	runCtx, cancelRun := context.WithTimeout(ctx, 2*time.Second)
	defer cancelRun()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- svc.Run(runCtx)
	}()

	if err := <-runErrCh; err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run service: %w", err)
	}

	if err := checkStats(ctx, log, svc, failed); err != nil {
		return fmt.Errorf("check stats: %w", err)
	}

	return nil
}

const jobNamePrint = "print_message"

type printPayload struct {
	Message string `json:"message"`
}

type printJob struct {
	sharedjob.DefaultJob
	log outboxlogger.Logger
}

func newPrintJob(log outboxlogger.Logger) *printJob {
	return &printJob{log: log}
}

func (j *printJob) Name() string {
	return jobNamePrint
}

func (j *printJob) Handle(ctx context.Context, payloadRaw string) error {
	var payload printPayload
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}

	jobID := outbox.JobIDFromContext(ctx)
	j.log.InfoContext(ctx, fmt.Sprintf("handled job: id=%s message=%q", jobID.String(), payload.Message))

	return nil
}

func (j *printJob) ExecutionTimeout() time.Duration {
	return 2 * time.Second
}

func (j *printJob) MaxAttempts() int {
	return 5
}

func checkStats(ctx context.Context, log outboxlogger.Logger, svc *outbox.Service, failed *jobsfailedrepo.Repo) error {
	stats, err := svc.GetQueueStats(ctx)
	if err != nil {
		return fmt.Errorf("queue stats: %w", err)
	}
	failedCount, err := failed.CountExact(ctx)
	if err != nil {
		return fmt.Errorf("failed count: %w", err)
	}

	log.InfoContext(ctx, fmt.Sprintf(
		"queue stats: total=%d available=%d processing=%d",
		stats.Total, stats.Available, stats.Processing,
	))
	log.InfoContext(ctx, fmt.Sprintf("failed jobs: %d", failedCount))

	return nil
}

func putDemoJobs(ctx context.Context, svc *outbox.Service) error {
	now := time.Now().UTC()

	payload1, err := json.Marshal(printPayload{Message: "hello from outbox #1"})
	if err != nil {
		return err
	}
	payload2, err := json.Marshal(printPayload{Message: "hello from outbox #2 (delayed)"})
	if err != nil {
		return err
	}

	if _, err := svc.Put(ctx, jobNamePrint, string(payload1), now); err != nil {
		return fmt.Errorf("put job #1: %w", err)
	}
	if _, err := svc.Put(ctx, jobNamePrint, string(payload2), now.Add(1200*time.Millisecond)); err != nil {
		return fmt.Errorf("put job #2: %w", err)
	}

	return nil
}

func resolvePostgresDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("OUTBOX_PG_DSN")); dsn != "" {
		return dsn
	}

	host := firstNonEmpty(
		os.Getenv("OUTBOX_PG_HOST"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_ADDRESS_LOCAL"),
		"127.0.0.1",
	)
	port := firstNonEmpty(
		os.Getenv("OUTBOX_PG_PORT"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_PORT_LOCAL"),
		"54335",
	)
	user := firstNonEmpty(
		os.Getenv("OUTBOX_PG_USER"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_USERNAME"),
		"tests-service",
	)
	password := firstNonEmpty(
		os.Getenv("OUTBOX_PG_PASSWORD"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_PASSWORD"),
		"tests-service",
	)
	dbName := firstNonEmpty(
		os.Getenv("OUTBOX_PG_DB"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_DATABASENAME"),
		"tests-db-pgsql",
	)
	sslMode := firstNonEmpty(
		os.Getenv("OUTBOX_PG_SSLMODE"),
		os.Getenv("TEST_OUTBOXLIB_PSQL_SSLMODE"),
		"disable",
	)

	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		user, password, host, port, dbName, sslMode,
	)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}

	return ""
}

func resetDemoData(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE jobs, jobs_failed;`); err != nil {
		return err
	}

	return nil
}
