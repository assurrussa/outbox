package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
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
		outbox.WithJobsFailedRepo(stubJobsRepo),
		outbox.WithTransactor(stubJobsRepo),
		outbox.WithAllowNonAtomicDLQ(),
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
	outbox.DefaultJob
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

func (j *stubRepo) CreateJobVersioned(
	_ context.Context,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	if err := (outbox.JobCapability{Name: name, SchemaVersion: schemaVersion}).Validate(); err != nil {
		return types.JobIDNil, err
	}
	jobID := types.NewJobID()
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.data = append(j.data, models.Job{
		ID:            jobID,
		Queue:         "queue",
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		Attempts:      0,
		ReservedAt:    sql.NullTime{},
		LeaseToken:    types.LeaseTokenNil,
		AvailableAt:   availableAt.UTC(),
		CreatedAt:     now,
	})
	return jobID, nil
}

func (j *stubRepo) FindAndReserveJobsForCapabilities(
	_ context.Context,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	capabilities []outbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	if err := leaseToken.Validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > outbox.MaxReservationBatchSize {
		return nil, fmt.Errorf("invalid reservation limit: %d", limit)
	}
	supported := make(map[outbox.JobCapability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, err
		}
		supported[capability] = struct{}{}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	indices := make([]int, 0, limit)
	for i := range j.data {
		job := j.data[i]
		capability := outbox.JobCapability{Name: job.Name, SchemaVersion: job.SchemaVersion}
		if _, ok := supported[capability]; !ok {
			continue
		}
		isAvailable := !job.AvailableAt.After(now)
		isNotReserved := !job.ReservedAt.Valid || !job.ReservedAt.Time.After(now)
		if !isAvailable || !isNotReserved {
			continue
		}
		indices = append(indices, i)
	}
	if len(indices) == 0 {
		return nil, sharederrors.ErrNoJobs
	}
	sort.Slice(indices, func(a, b int) bool {
		left, right := j.data[indices[a]], j.data[indices[b]]
		if left.AvailableAt.Equal(right.AvailableAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.AvailableAt.Before(right.AvailableAt)
	})
	if len(indices) > limit {
		indices = indices[:limit]
	}

	jobs := make([]models.Job, 0, len(indices))
	for _, index := range indices {
		j.data[index].Attempts++
		j.data[index].ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
		j.data[index].LeaseToken = leaseToken
		jobs = append(jobs, j.data[index])
	}
	return jobs, nil
}

func (j *stubRepo) ExtendJobLeases(
	_ context.Context,
	jobIDs []types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	wanted := jobIDSet(jobIDs)
	j.mu.Lock()
	defer j.mu.Unlock()
	var affected int64
	for i := range j.data {
		job := j.data[i]
		if _, ok := wanted[job.ID]; !ok || job.LeaseToken != leaseToken ||
			!job.ReservedAt.Valid || !job.ReservedAt.Time.After(now) {
			continue
		}
		j.data[i].ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
		affected++
	}
	return affected, nil
}

func (j *stubRepo) ReleaseUnstartedJobsWithLease(
	_ context.Context,
	jobIDs []types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	wanted := jobIDSet(jobIDs)
	j.mu.Lock()
	defer j.mu.Unlock()
	var affected int64
	for i := range j.data {
		job := j.data[i]
		if _, ok := wanted[job.ID]; !ok || job.LeaseToken != leaseToken ||
			!job.ReservedAt.Valid || !job.ReservedAt.Time.After(now) || job.Attempts < 1 {
			continue
		}
		j.data[i].Attempts--
		j.data[i].ReservedAt = sql.NullTime{}
		j.data[i].LeaseToken = types.LeaseTokenNil
		affected++
	}
	return affected, nil
}

func (j *stubRepo) DeleteJobWithLease(
	_ context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, job := range j.data {
		if job.ID == jobID && job.LeaseToken == leaseToken &&
			job.ReservedAt.Valid && job.ReservedAt.Time.After(now) {
			j.data = append(j.data[:i], j.data[i+1:]...)
			return 1, nil
		}
	}
	return 0, nil
}

func (j *stubRepo) RescheduleJobWithLease(
	_ context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.data {
		job := j.data[i]
		if job.ID == jobID && job.LeaseToken == leaseToken &&
			job.ReservedAt.Valid && job.ReservedAt.Time.After(now) {
			j.data[i].AvailableAt = availableAt.UTC()
			j.data[i].ReservedAt = sql.NullTime{}
			j.data[i].LeaseToken = types.LeaseTokenNil
			return 1, nil
		}
	}
	return 0, nil
}

func (*stubRepo) MaxReservationBatchSize() int { return outbox.MaxReservationBatchSize }

func (j *stubRepo) CreateFailedJobVersioned(
	_ context.Context,
	jobID types.JobID,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	reason string,
) (types.JobID, error) {
	failedJobID := types.NewJobID()
	now := time.Now().UTC()

	j.mu.Lock()
	defer j.mu.Unlock()
	j.dataFailed = append(j.dataFailed, models.JobFailed{
		ID:            failedJobID,
		JobID:         jobID,
		Queue:         "queue",
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		Reason:        reason,
		FailedAt:      now,
		CreatedAt:     now,
	})
	return failedJobID, nil
}

func (j *stubRepo) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (j *stubRepo) SupportsAtomicDLQ() bool {
	return false
}

func (j *stubRepo) GetQueueStats(
	_ context.Context,
	observedAt time.Time,
) (outbox.QueueStats, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	observedAt = observedAt.UTC()
	groups := make(map[outbox.JobCapability]*outbox.CapabilityQueueStats)
	stats := outbox.QueueStats{ObservedAt: observedAt, Total: int64(len(j.data))}
	for _, job := range j.data {
		capability := outbox.JobCapability{Name: job.Name, SchemaVersion: job.SchemaVersion}
		group := groups[capability]
		if group == nil {
			group = &outbox.CapabilityQueueStats{Name: job.Name, SchemaVersion: job.SchemaVersion}
			groups[capability] = group
		}
		group.Total++
		if job.ReservedAt.Valid && job.ReservedAt.Time.After(observedAt) {
			group.Processing++
			stats.Processing++
			continue
		}
		if !job.AvailableAt.After(observedAt) {
			group.Available++
			stats.Available++
			if group.OldestAvailableAt.IsZero() || job.AvailableAt.Before(group.OldestAvailableAt) {
				group.OldestAvailableAt = job.AvailableAt.UTC()
			}
		}
	}
	for _, group := range groups {
		stats.ByCapability = append(stats.ByCapability, *group)
	}
	return stats, nil
}

func jobIDSet(jobIDs []types.JobID) map[types.JobID]struct{} {
	result := make(map[types.JobID]struct{}, len(jobIDs))
	for _, jobID := range jobIDs {
		result[jobID] = struct{}{}
	}
	return result
}
