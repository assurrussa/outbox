//nolint:testpackage // The benchmark intentionally measures the internal execution scheduler.
package outbox

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const executionBenchmarkJobsPerOperation = 100

//nolint:gocognit // One benchmark matrix keeps setup and per-job normalization identical across paths.
func BenchmarkExecutionPaths(b *testing.B) {
	benchmarks := []struct {
		name     string
		batchMax int
	}{
		{name: "single", batchMax: 0},
		{name: "true-batch-1", batchMax: 1},
		{name: "true-batch-100", batchMax: 100},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			repo := newExecutionBenchmarkRepo()
			service := newExecutionBenchmarkService(repo, benchmark.batchMax)
			capability := JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion}
			ctx := context.Background()
			log := logger.Discard()

			runtime.GC()
			var memoryBefore runtime.MemStats
			var memoryAfter runtime.MemStats
			runtime.ReadMemStats(&memoryBefore)
			b.ResetTimer()
			for range b.N {
				repo.reset(executionBenchmarkJobsPerOperation)
				if benchmark.batchMax == 0 {
					for range executionBenchmarkJobsPerOperation {
						if err := service.findAndProcessBatch(ctx, log, []JobCapability{capability}); err != nil {
							b.Fatal(err)
						}
					}
					continue
				}
				for repo.remainingJobs() > 0 {
					processed, err := service.findAndProcessExecutionBatch(ctx, log, capability)
					if err != nil {
						b.Fatal(err)
					}
					if !processed {
						b.Fatal("execution batch did not process an available job")
					}
				}
			}
			b.StopTimer()
			runtime.ReadMemStats(&memoryAfter)

			jobs := float64(b.N * executionBenchmarkJobsPerOperation)
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/jobs, "ns/job")
			b.ReportMetric(float64(memoryAfter.TotalAlloc-memoryBefore.TotalAlloc)/jobs, "B/job")
			b.ReportMetric(float64(memoryAfter.Mallocs-memoryBefore.Mallocs)/jobs, "allocs/job")
			b.ReportMetric(float64(repo.claimCalls.Load())/jobs, "claims/job")
			b.ReportMetric(float64(repo.handlerCalls.Load())/jobs, "handler-calls/job")
			b.ReportMetric(float64(repo.finalizationCalls.Load())/jobs, "finalizations/job")
		})
	}
}

type executionBenchmarkRepo struct {
	*executionBatchTestRepo
	remaining         atomic.Int64
	claimCalls        atomic.Int64
	handlerCalls      atomic.Int64
	finalizationCalls atomic.Int64
}

func newExecutionBenchmarkRepo() *executionBenchmarkRepo {
	return &executionBenchmarkRepo{executionBatchTestRepo: &executionBatchTestRepo{}}
}

func (r *executionBenchmarkRepo) reset(jobs int) {
	r.remaining.Store(int64(jobs))
}

func (r *executionBenchmarkRepo) remainingJobs() int64 {
	return r.remaining.Load()
}

func (r *executionBenchmarkRepo) FindAndReserveJobsForCapabilities(
	_ context.Context,
	_, until time.Time,
	leaseToken LeaseToken,
	_ []JobCapability,
	limit int,
) ([]models.Job, error) {
	return r.claim(leaseToken, until, limit)
}

func (r *executionBenchmarkRepo) FindAndReserveJobsForCapability(
	_ context.Context,
	_, until time.Time,
	leaseToken LeaseToken,
	_ JobCapability,
	limit int,
) ([]models.Job, error) {
	return r.claim(leaseToken, until, limit)
}

func (r *executionBenchmarkRepo) FindAndReserveJobsForCapabilityBounded(
	_ context.Context,
	_, until time.Time,
	leaseToken LeaseToken,
	_ JobCapability,
	limits BatchClaimLimits,
) ([]models.Job, error) {
	return r.claim(leaseToken, until, limits.MaxMessages)
}

func (r *executionBenchmarkRepo) claim(
	leaseToken LeaseToken,
	reservedUntil time.Time,
	limit int,
) ([]models.Job, error) {
	r.claimCalls.Add(1)
	remaining := r.remaining.Load()
	if remaining == 0 {
		return nil, ErrNoJobs
	}
	count := min(int(remaining), limit)
	jobs := make([]models.Job, count)
	for index := range jobs {
		jobs[index] = executionBatchTestJob(testBatchJobName, leaseToken)
		jobs[index].ReservedAt.Time = reservedUntil
	}
	r.remaining.Add(-int64(count))
	return jobs, nil
}

func (r *executionBenchmarkRepo) DeleteJobWithLease(
	context.Context,
	types.JobID,
	LeaseToken,
	time.Time,
) (int64, error) {
	r.finalizationCalls.Add(1)
	return 1, nil
}

func (r *executionBenchmarkRepo) ApplyBatchJobOutcomes(
	_ context.Context,
	_ LeaseToken,
	_ time.Time,
	outcomes []BatchJobOutcome,
) (int64, error) {
	r.finalizationCalls.Add(1)
	return int64(len(outcomes)), nil
}

type executionBenchmarkSingleHandler struct{ calls *atomic.Int64 }

func (*executionBenchmarkSingleHandler) Name() string { return testBatchJobName }

func (h *executionBenchmarkSingleHandler) Handle(context.Context, string) error {
	h.calls.Add(1)
	return nil
}

func (*executionBenchmarkSingleHandler) ExecutionTimeout() time.Duration { return time.Hour }

func (*executionBenchmarkSingleHandler) MaxAttempts() int { return 3 }

type executionBenchmarkBatchHandler struct{ calls *atomic.Int64 }

func (*executionBenchmarkBatchHandler) Name() string { return testBatchJobName }

func (h *executionBenchmarkBatchHandler) HandleBatch(
	_ context.Context,
	items []BatchJobItem,
) (BatchResult, error) {
	h.calls.Add(1)
	return successfulExecutionBatchResult(items), nil
}

func (*executionBenchmarkBatchHandler) ExecutionTimeout() time.Duration { return time.Hour }

func (*executionBenchmarkBatchHandler) MaxAttempts() int { return 3 }

func newExecutionBenchmarkService(repo *executionBenchmarkRepo, batchMax int) *Service {
	service := newExecutionBatchTestService(
		repo.executionBatchTestRepo,
		&executionBatchTestFailedRepo{},
		&executionBatchTestTransactor{},
	)
	service.jobsRepo = repo
	service.reservationBatchSize = 1
	if batchMax == 0 {
		service.MustRegisterJob(&executionBenchmarkSingleHandler{calls: &repo.handlerCalls})
		return service
	}
	service.MustRegisterBatchJob(
		&executionBenchmarkBatchHandler{calls: &repo.handlerCalls},
		BatchConfig{MaxMessages: batchMax, MaxBytes: 4 << 20, MaxWait: 25 * time.Millisecond},
	)
	return service
}
