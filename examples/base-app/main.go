package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	sharedjob "github.com/assurrussa/outbox/shared/job"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func main() {
	ctx := context.Background()
	lg := outboxlogger.DefaultText().Named("base-app")

	if err := run(ctx, lg); err != nil {
		lg.ErrorContext(ctx, "base-app error", outboxlogger.Error(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, log outboxlogger.Logger) error {
	stubJobsRepo := &stubRepo{}
	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(200*time.Millisecond),
		outbox.WithReserveFor(5*time.Second),
		outbox.WithJobsRepo(stubJobsRepo),
		outbox.WithJobsStatRepo(stubJobsRepo),
		outbox.WithJobsFailedRepo(stubJobsRepo),
		outbox.WithTransactor(stubJobsRepo),
		outbox.WithLogger(log),
	)
	if err != nil {
		return fmt.Errorf("create outbox service: %w", err)
	}

	svc.MustRegisterJob(newPrintJob(log))

	if err := putDemoJobs(ctx, svc); err != nil {
		return err
	}

	if err := checkStats(ctx, log, svc); err != nil {
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

	if err := checkStats(ctx, log, svc); err != nil {
		return fmt.Errorf("check stats: %w", err)
	}

	return nil
}

func checkStats(ctx context.Context, log outboxlogger.Logger, svc *outbox.Service) error {
	stats, err := svc.GetQueueStats(ctx)
	if err != nil {
		return fmt.Errorf("queue stats: %w", err)
	}

	log.InfoContext(ctx, fmt.Sprintf(
		"queue stats: total=%d available=%d processing=%d",
		stats.Total, stats.Available, stats.Processing,
	))

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

type stubRepo struct {
	data       []models.Job
	dataFailed []models.JobFailed
	mu         sync.Mutex
}

func (j *stubRepo) CreateJob(
	_ context.Context, name string, payload string, availableAt time.Time,
) (types.JobID, error) {
	jobID := types.NewJobID()
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.data = append(j.data, models.Job{
		ID:          jobID,
		Queue:       "queue",
		Name:        name,
		Payload:     payload,
		Attempts:    0,
		ReservedAt:  sql.NullTime{},
		AvailableAt: availableAt,
		CreatedAt:   now,
	})
	return jobID, nil
}
func (j *stubRepo) FindAndReserveJob(_ context.Context, now time.Time, until time.Time) (models.Job, error) {
	j.mu.Lock()
	data := j.data
	j.mu.Unlock()

	bestIdx := -1
	for i := range data {
		job := data[i]
		isAvailable := !job.AvailableAt.After(now)
		isNotReserved := !job.ReservedAt.Valid || !job.ReservedAt.Time.After(now)
		if !isAvailable || !isNotReserved {
			continue
		}

		if bestIdx == -1 {
			bestIdx = i
			continue
		}

		best := data[bestIdx]
		if job.AvailableAt.Before(best.AvailableAt) ||
			(job.AvailableAt.Equal(best.AvailableAt) && job.CreatedAt.Before(best.CreatedAt)) {
			bestIdx = i
		}
	}

	if bestIdx == -1 {
		return models.Job{}, sharederrors.ErrNoJobs
	}

	data[bestIdx].Attempts++
	data[bestIdx].ReservedAt = sql.NullTime{
		Time:  until,
		Valid: true,
	}

	return data[bestIdx], nil
}

func (j *stubRepo) DeleteJob(_ context.Context, jobID types.JobID) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, job := range j.data {
		if job.ID == jobID {
			j.data = append(j.data[:i], j.data[i+1:]...)
			return 1, nil
		}
	}

	return 0, nil
}

func (j *stubRepo) CreateFailedJob(_ context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error) {
	failedJobID := types.NewJobID()
	now := time.Now().UTC()

	j.mu.Lock()
	defer j.mu.Unlock()
	j.dataFailed = append(j.dataFailed, models.JobFailed{
		ID:        failedJobID,
		JobID:     jobID,
		Queue:     "queue",
		Name:      name,
		Payload:   payload,
		Reason:    reason,
		FailedAt:  now,
		CreatedAt: now,
	})
	return failedJobID, nil
}

func (j *stubRepo) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (j *stubRepo) CountExact(_ context.Context) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	return int64(len(j.data)), nil
}

func (j *stubRepo) CountAvailable(_ context.Context, now time.Time) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	var count int64
	for i := range j.data {
		job := j.data[i]
		if !job.AvailableAt.After(now) && (!job.ReservedAt.Valid || !job.ReservedAt.Time.After(now)) {
			count++
		}
	}

	return count, nil
}

func (j *stubRepo) CountReserved(_ context.Context, now time.Time) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	var count int64
	for i := range j.data {
		job := j.data[i]
		if job.ReservedAt.Valid && job.ReservedAt.Time.After(now) {
			count++
		}
	}

	return count, nil
}
