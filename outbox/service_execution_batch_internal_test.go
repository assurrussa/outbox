package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	testBatchJobName  = "batch"
	testBatchAJobName = "batch-a"
	testBatchBJobName = "batch-b"
	testSingleJobName = "single"
)

func TestExecutionBatchRejectsClaimedDifferentCapability(t *testing.T) {
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		return []models.Job{executionBatchTestJob(testBatchBJobName, leaseToken)}, nil
	}
	handler := &executionBatchTestHandler{name: testBatchAJobName}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchAJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if processed || !errors.Is(err, ErrUnsupportedClaim) {
		t.Fatalf("find and process mismatched capability = (%v, %v), want (false, %v)", processed, err, ErrUnsupportedClaim)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("mismatched capability entered handler %d times, want 0", got)
	}
	if got := repo.applyCalls.Load(); got != 0 {
		t.Fatalf("mismatched capability finalized %d times, want 0", got)
	}
}

func TestExecutionBatchHandlerTimeoutDefersClaimedJobs(t *testing.T) {
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
	}
	handler := &executionBatchTestHandler{
		name:    testBatchJobName,
		timeout: 10 * time.Millisecond,
		handle: func(ctx context.Context, items []BatchJobItem) (BatchResult, error) {
			<-ctx.Done()
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1})
	var logs bytes.Buffer

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.DefaultJSONWithWriter(&logs),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process timed-out batch = (%v, %v), want successful deferral", processed, err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.outcomes) != 1 || len(repo.outcomes[0]) != 1 || repo.outcomes[0][0].Kind != BatchJobOutcomeDefer {
		t.Fatalf("timed-out batch outcomes = %#v, want one defer", repo.outcomes)
	}
	if output := logs.String(); !strings.Contains(output, `"msg":"handle batch job error"`) ||
		!strings.Contains(output, `"batch_size":1`) {
		t.Fatalf("timed-out batch log = %s, want top-level error and batch size", output)
	}
}

func TestExecutionBatchMaxWaitFlushesWhileFillClaimIsBlocked(t *testing.T) {
	for _, test := range []struct {
		name    string
		bounded bool
	}{
		{name: "singleton fallback"},
		{name: "bounded repository", bounded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseRepo := &executionBatchTestRepo{}
			var findCalls atomic.Int32
			fillDeadlineObserved := make(chan struct{}, 1)
			claim := func(ctx context.Context, leaseToken LeaseToken) ([]models.Job, error) {
				if findCalls.Add(1) == 1 {
					return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
				}
				<-ctx.Done()
				fillDeadlineObserved <- struct{}{}
				return nil, ctx.Err()
			}

			var repo JobsRepository = baseRepo
			if test.bounded {
				baseRepo.findBatch = func(
					context.Context,
					JobCapability,
					LeaseToken,
					int,
				) ([]models.Job, error) {
					t.Fatal("bounded repository unexpectedly used singleton fallback")
					return nil, ErrNoJobs
				}
				boundedRepo := &boundedExecutionBatchTestRepo{executionBatchTestRepo: baseRepo}
				boundedRepo.findBounded = func(
					ctx context.Context,
					_ JobCapability,
					leaseToken LeaseToken,
					_ BatchClaimLimits,
				) ([]models.Job, error) {
					return claim(ctx, leaseToken)
				}
				repo = boundedRepo
			} else {
				baseRepo.findBatch = func(
					ctx context.Context,
					_ JobCapability,
					leaseToken LeaseToken,
					_ int,
				) ([]models.Job, error) {
					return claim(ctx, leaseToken)
				}
			}

			handledItems := make(chan int, 1)
			handler := &executionBatchTestHandler{
				name: testBatchJobName,
				handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
					handledItems <- len(items)
					return successfulExecutionBatchResult(items), nil
				},
			}
			service := newExecutionBatchTestService(
				repo,
				&executionBatchTestFailedRepo{},
				&executionBatchTestTransactor{},
			)
			service.MustRegisterBatchJob(handler, BatchConfig{
				MaxMessages: 2,
				MaxBytes:    1024,
				MaxWait:     10 * time.Millisecond,
			})

			runCtx, cancelRun := context.WithTimeout(t.Context(), time.Second)
			defer cancelRun()
			processed, err := service.findAndProcessExecutionBatch(
				runCtx,
				logger.Discard(),
				JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
			)
			if err != nil || !processed {
				t.Fatalf("find and process batch = (%v, %v), want a normal MaxWait flush", processed, err)
			}
			if cause := context.Cause(runCtx); cause != nil {
				t.Fatalf("parent context ended before fill flush: %v", cause)
			}
			select {
			case <-fillDeadlineObserved:
			default:
				t.Fatal("supplemental claim did not observe the fill deadline")
			}
			if handled := <-handledItems; handled != 1 {
				t.Fatalf("handled items = %d, want the one job collected before MaxWait", handled)
			}
			if calls := findCalls.Load(); calls != 2 {
				t.Fatalf("claim calls = %d, want initial and one deadline-bounded supplemental claim", calls)
			}
		})
	}
}

func TestExecutionBatchMaxWaitFlushesOnDriverCancellationError(t *testing.T) {
	repo := &executionBatchTestRepo{}
	var findCalls atomic.Int32
	repo.findBatch = func(
		ctx context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		if findCalls.Add(1) == 1 {
			return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
		}
		<-ctx.Done()
		return nil, errors.New("canceling statement due to user request (SQLSTATE 57014)")
	}

	handledItems := make(chan int, 1)
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handledItems <- len(items)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(
		repo,
		&executionBatchTestFailedRepo{},
		&executionBatchTestTransactor{},
	)
	service.MustRegisterBatchJob(handler, BatchConfig{
		MaxMessages: 2,
		MaxBytes:    1024,
		MaxWait:     10 * time.Millisecond,
	})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process batch = (%v, %v), want a normal MaxWait flush", processed, err)
	}
	if handled := <-handledItems; handled != 1 {
		t.Fatalf("handled items = %d, want the one job collected before MaxWait", handled)
	}
	if calls := findCalls.Load(); calls != 2 {
		t.Fatalf("claim calls = %d, want initial and one deadline-bounded supplemental claim", calls)
	}
}

func TestExecutionBatchSkipsSupplementalClaimWithoutMinimumBudget(t *testing.T) {
	baseRepo := &executionBatchTestRepo{}
	baseRepo.findBatch = func(
		context.Context,
		JobCapability,
		LeaseToken,
		int,
	) ([]models.Job, error) {
		t.Fatal("bounded repository unexpectedly used singleton fallback")
		return nil, ErrNoJobs
	}
	boundedRepo := &boundedExecutionBatchTestRepo{executionBatchTestRepo: baseRepo}
	var findCalls atomic.Int32
	boundedRepo.findBounded = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ BatchClaimLimits,
	) ([]models.Job, error) {
		if call := findCalls.Add(1); call != 1 {
			t.Fatalf("bounded claim call = %d, want only the initial claim", call)
		}
		return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
	}

	handledItems := make(chan int, 1)
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handledItems <- len(items)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(
		boundedRepo,
		&executionBatchTestFailedRepo{},
		&executionBatchTestTransactor{},
	)
	service.MustRegisterBatchJob(handler, BatchConfig{
		MaxMessages: 2,
		MaxBytes:    1024,
		MaxWait:     batchFillClaimHeadroom / 2,
	})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process batch = (%v, %v), want a normal early flush", processed, err)
	}
	if handled := <-handledItems; handled != 1 {
		t.Fatalf("handled items = %d, want the initial job", handled)
	}
	if calls := findCalls.Load(); calls != 1 {
		t.Fatalf("bounded claim calls = %d, want 1", calls)
	}
}

func TestExecutionBatchRestoresDurableOrderAfterSupplementalClaims(t *testing.T) {
	repo := &executionBatchTestRepo{}
	var findCalls atomic.Int32
	queueTime := time.Now().UTC()
	var earlierJob models.Job
	var laterJob models.Job
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		switch findCalls.Add(1) {
		case 1:
			laterJob = executionBatchTestJob(testBatchJobName, leaseToken)
			laterJob.AvailableAt = queueTime.Add(time.Second)
			laterJob.CreatedAt = queueTime.Add(time.Second)
			return []models.Job{laterJob}, nil
		case 2:
			earlierJob = executionBatchTestJob(testBatchJobName, leaseToken)
			earlierJob.AvailableAt = queueTime
			earlierJob.CreatedAt = queueTime
			return []models.Job{earlierJob}, nil
		default:
			return nil, ErrNoJobs
		}
	}

	var handled []BatchJobItem
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handled = append([]BatchJobItem(nil), items...)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 2, MaxBytes: 1024, MaxWait: time.Second})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process reordered batch = (%v, %v), want success", processed, err)
	}
	if len(handled) != 2 || handled[0].JobID != earlierJob.ID || handled[1].JobID != laterJob.ID {
		t.Fatalf("handler order = %#v, want durable order [%s, %s]", handled, earlierJob.ID, laterJob.ID)
	}
}

func TestExecutionBatchResultMappingIgnoresHandlerInputMutation(t *testing.T) {
	repo := &executionBatchTestRepo{}
	failed := &executionBatchTestFailedRepo{}
	var findCalls atomic.Int32
	var firstJob models.Job
	var secondJob models.Job
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		switch findCalls.Add(1) {
		case 1:
			firstJob = executionBatchTestJob(testBatchJobName, leaseToken)
			firstJob.AvailableAt = time.Unix(1, 0).UTC()
			firstJob.CreatedAt = time.Unix(1, 0).UTC()
			return []models.Job{firstJob}, nil
		case 2:
			secondJob = executionBatchTestJob(testBatchJobName, leaseToken)
			secondJob.AvailableAt = time.Unix(2, 0).UTC()
			secondJob.CreatedAt = time.Unix(2, 0).UTC()
			return []models.Job{secondJob}, nil
		default:
			return nil, ErrNoJobs
		}
	}
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			items[0], items[1] = items[1], items[0]
			return BatchResult{Items: []BatchItemResult{
				{JobID: items[0].JobID, Err: Permanent(errors.New("invalid second payload"))},
				{JobID: items[1].JobID},
			}}, nil
		},
	}
	service := newExecutionBatchTestService(repo, failed, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 2, MaxBytes: 1024, MaxWait: time.Second})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process mutated-input batch = (%v, %v), want success", processed, err)
	}

	repo.mu.Lock()
	if len(repo.outcomes) != 1 || len(repo.outcomes[0]) != 2 {
		repo.mu.Unlock()
		t.Fatalf("batch outcomes = %#v, want one two-item outcome set", repo.outcomes)
	}
	outcomes := append([]BatchJobOutcome(nil), repo.outcomes[0]...)
	repo.mu.Unlock()
	kinds := make(map[types.JobID]BatchJobOutcomeKind, len(outcomes))
	for _, outcome := range outcomes {
		kinds[outcome.JobID] = outcome.Kind
	}
	if got := kinds[firstJob.ID]; got != BatchJobOutcomeSuccess {
		t.Fatalf("first job outcome = %v, want success", got)
	}
	if got := kinds[secondJob.ID]; got != BatchJobOutcomeDLQ {
		t.Fatalf("second job outcome = %v, want DLQ", got)
	}
	failed.mu.Lock()
	defer failed.mu.Unlock()
	if len(failed.jobs) != 1 || failed.jobs[0].JobID != secondJob.ID {
		t.Fatalf("failed jobs = %#v, want only second job %s", failed.jobs, secondJob.ID)
	}
}

func TestExecutionBatchCollectorClaimsOneCandidateAtATime(t *testing.T) {
	repo := &executionBatchTestRepo{}
	payloads := []string{"a", "b", "ccc"}
	claimLimits := make([]int, 0, len(payloads))
	findCall := 0
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		limit int,
	) ([]models.Job, error) {
		claimLimits = append(claimLimits, limit)
		job := executionBatchTestJob(testBatchJobName, leaseToken)
		job.Payload = payloads[findCall]
		findCall++
		return []models.Job{job}, nil
	}
	var handled []BatchJobItem
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handled = append([]BatchJobItem(nil), items...)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 4, MaxBytes: 3, MaxWait: time.Second})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process bounded collector batch = (%v, %v), want success", processed, err)
	}
	if len(claimLimits) != 3 {
		t.Fatalf("collector claims = %v, want three singleton candidate claims", claimLimits)
	}
	for _, limit := range claimLimits {
		if limit != batchCollectorClaimLimit {
			t.Fatalf("collector claim limits = %v, want only %d", claimLimits, batchCollectorClaimLimit)
		}
	}
	if len(handled) != 2 {
		t.Fatalf("handler items = %d, want the two payloads within MaxBytes", len(handled))
	}
	if got := repo.releaseCalls.Load(); got != 1 {
		t.Fatalf("collector released %d byte-tail claims, want 1", got)
	}
}

func TestExecutionBatchUsesBoundedInitialClaim(t *testing.T) {
	baseRepo := &executionBatchTestRepo{}
	baseRepo.findBatch = func(
		context.Context,
		JobCapability,
		LeaseToken,
		int,
	) ([]models.Job, error) {
		t.Fatal("bounded repository unexpectedly used singleton fallback")
		return nil, ErrNoJobs
	}
	var gotLimits BatchClaimLimits
	var boundedCalls atomic.Int32
	boundedRepo := &boundedExecutionBatchTestRepo{executionBatchTestRepo: baseRepo}
	boundedRepo.findBounded = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		limits BatchClaimLimits,
	) ([]models.Job, error) {
		boundedCalls.Add(1)
		gotLimits = limits
		jobs := make([]models.Job, 3)
		for index := range jobs {
			jobs[index] = executionBatchTestJob(testBatchJobName, leaseToken)
			jobs[index].Payload = "payload"
		}
		return jobs, nil
	}

	var handled int
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handled = len(items)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(boundedRepo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 3, MaxBytes: 64, MaxWait: time.Second})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process bounded batch = (%v, %v), want success", processed, err)
	}
	if gotLimits != (BatchClaimLimits{MaxMessages: 3, MaxBytes: 64}) {
		t.Fatalf("initial bounded limits = %#v, want count=3 bytes=64", gotLimits)
	}
	if calls := boundedCalls.Load(); calls != 1 {
		t.Fatalf("bounded claim calls = %d, want 1", calls)
	}
	if handled != 3 {
		t.Fatalf("handled items = %d, want 3", handled)
	}
}

func TestExecutionBatchBoundedFillUsesRemainingLimitsAndReleasesOversizedTail(t *testing.T) {
	baseRepo := &executionBatchTestRepo{}
	boundedRepo := &boundedExecutionBatchTestRepo{executionBatchTestRepo: baseRepo}
	limits := make([]BatchClaimLimits, 0, 3)
	boundedRepo.findBounded = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		claimLimits BatchClaimLimits,
	) ([]models.Job, error) {
		limits = append(limits, claimLimits)
		job := executionBatchTestJob(testBatchJobName, leaseToken)
		job.AvailableAt = time.Unix(int64(len(limits)), 0).UTC()
		job.CreatedAt = job.AvailableAt
		switch len(limits) {
		case 1:
			job.Payload = "a"
		case 2:
			job.Payload = "bb"
		case 3:
			job.Payload = "é"
		default:
			return nil, ErrNoJobs
		}
		return []models.Job{job}, nil
	}

	var handled []BatchJobItem
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			handled = append([]BatchJobItem(nil), items...)
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(boundedRepo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 3, MaxBytes: 4, MaxWait: time.Second})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process bounded byte batch = (%v, %v), want success", processed, err)
	}
	wantLimits := []BatchClaimLimits{
		{MaxMessages: 3, MaxBytes: 4},
		{MaxMessages: 2, MaxBytes: 3},
		{MaxMessages: 1, MaxBytes: 1},
	}
	if !slices.Equal(limits, wantLimits) {
		t.Fatalf("bounded limits = %#v, want %#v", limits, wantLimits)
	}
	if len(handled) != 2 || handled[0].Payload != "a" || handled[1].Payload != "bb" {
		t.Fatalf("handler items = %#v, want payloads a and bb", handled)
	}
	if got := baseRepo.releaseCalls.Load(); got != 1 {
		t.Fatalf("released byte-tail claims = %d, want 1", got)
	}
}

func TestValidateBoundedExecutionBatchPayload(t *testing.T) {
	job := func(payload string) models.Job {
		return models.Job{ID: types.NewJobID(), Payload: payload}
	}
	tests := []struct {
		name     string
		jobs     []models.Job
		maxBytes int
		wantErr  bool
	}{
		{name: "within limit", jobs: []models.Job{job("a"), job("é")}, maxBytes: 3},
		{name: "oversized singleton", jobs: []models.Job{job("é")}, maxBytes: 1},
		{name: "job after oversized first", jobs: []models.Job{job("é"), job("a")}, maxBytes: 1, wantErr: true},
		{name: "cumulative overflow", jobs: []models.Job{job("aa"), job("bb")}, maxBytes: 3, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateBoundedExecutionBatchPayload(test.jobs, test.maxBytes)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate bounded payload error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestFillExecutionBatchHeartbeatsSelectedJobsAndStopsAtByteTail(t *testing.T) {
	releaseStarted := make(chan struct{})
	releaseContinue := make(chan struct{})
	extended := make(chan []types.JobID, 16)
	var releaseOnce sync.Once
	var unblockOnce sync.Once
	t.Cleanup(func() { unblockOnce.Do(func() { close(releaseContinue) }) })
	var findCalls atomic.Int32

	repo := &executionBatchTestRepo{
		onExtend: func(jobIDs []types.JobID) {
			select {
			case extended <- append([]types.JobID(nil), jobIDs...):
			default:
			}
		},
		onRelease: func(_ []types.JobID) {
			releaseOnce.Do(func() {
				close(releaseStarted)
				<-releaseContinue
			})
		},
	}
	leaseToken := types.NewLeaseToken()
	initialJob := executionBatchTestJob(testBatchJobName, leaseToken)
	initialJob.Payload = "a"
	selectedJob := executionBatchTestJob(testBatchJobName, leaseToken)
	selectedJob.Payload = "bb"
	tailJob := executionBatchTestJob(testBatchJobName, leaseToken)
	tailJob.Payload = "cc"
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		_ LeaseToken,
		limit int,
	) ([]models.Job, error) {
		if limit != batchCollectorClaimLimit {
			t.Errorf("fill claim limit = %d, want %d", limit, batchCollectorClaimLimit)
		}
		switch findCalls.Add(1) {
		case 1:
			return []models.Job{selectedJob}, nil
		case 2:
			return []models.Job{tailJob}, nil
		default:
			return nil, ErrNoJobs
		}
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.reserveFor = 60 * time.Millisecond
	initialJob.ReservedAt.Time = time.Now().UTC().Add(service.reserveFor)
	selectedJob.ReservedAt.Time = initialJob.ReservedAt.Time
	batchCtx, cancelBatch := context.WithCancelCause(t.Context())
	manager := newBatchLeaseManager(
		batchCtx,
		repo,
		[]models.Job{initialJob},
		leaseToken,
		service.reserveFor,
		cancelBatch,
	)

	result := make(chan struct {
		jobs []models.Job
		err  error
	}, 1)
	go func() {
		jobs, err := service.fillExecutionBatch(
			batchCtx,
			repo,
			manager,
			JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
			normalizedBatchConfig{maxMessages: 3, maxBytes: 4, maxWait: 100 * time.Millisecond},
			[]models.Job{initialJob},
		)
		result <- struct {
			jobs []models.Job
			err  error
		}{jobs: jobs, err: err}
	}()

	select {
	case <-releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("fill byte-limit tail release did not start")
	}
	waitForExtendedJobIDs(t, extended, initialJob.ID, selectedJob.ID)
	unblockOnce.Do(func() { close(releaseContinue) })
	var fillResult struct {
		jobs []models.Job
		err  error
	}
	select {
	case fillResult = <-result:
	case <-time.After(time.Second):
		t.Fatal("fill did not finish after releasing the byte-limit tail")
	}
	cancelBatch(nil)
	if heartbeatErr := manager.stopAndWait(); heartbeatErr != nil {
		t.Fatalf("heartbeat error = %v", heartbeatErr)
	}
	if fillResult.err != nil {
		t.Fatalf("fill execution batch error = %v", fillResult.err)
	}
	if len(fillResult.jobs) != 2 {
		t.Fatalf("filled jobs = %d, want 2", len(fillResult.jobs))
	}
	if got := findCalls.Load(); got != 2 {
		t.Fatalf("fill claims = %d, want selected and byte-limit-tail candidates", got)
	}
}

func TestExecutionBatchStartedJobsRemainLeasedWhenFinalizationFails(t *testing.T) {
	invalidResultErr := ErrInvalidBatchResult
	permanentErr := errors.New("permanent top-level failure")
	applyErr := errors.New("apply outcomes failed")
	tests := []struct {
		name           string
		handle         func(context.Context, []BatchJobItem) (BatchResult, error)
		applyErr       error
		wantErr        error
		wantApplyCalls int32
	}{
		{
			name: "invalid result",
			handle: func(context.Context, []BatchJobItem) (BatchResult, error) {
				return BatchResult{}, nil
			},
			wantErr: invalidResultErr,
		},
		{
			name: "permanent top-level failure",
			handle: func(context.Context, []BatchJobItem) (BatchResult, error) {
				return BatchResult{}, Permanent(permanentErr)
			},
			wantErr: permanentErr,
		},
		{
			name: "outcome transaction failure",
			handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
				return successfulExecutionBatchResult(items), nil
			},
			applyErr:       applyErr,
			wantErr:        applyErr,
			wantApplyCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &executionBatchTestRepo{applyErr: test.applyErr}
			repo.findBatch = func(
				_ context.Context,
				_ JobCapability,
				leaseToken LeaseToken,
				_ int,
			) ([]models.Job, error) {
				return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
			}
			handler := &executionBatchTestHandler{name: testBatchJobName, handle: test.handle}
			service := newExecutionBatchTestService(
				repo,
				&executionBatchTestFailedRepo{},
				&executionBatchTestTransactor{},
			)
			service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1})

			processed, err := service.findAndProcessExecutionBatch(
				t.Context(),
				logger.Discard(),
				JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
			)
			if !processed || !errors.Is(err, test.wantErr) {
				t.Fatalf("find and process failed finalization = (%v, %v), want (true, %v)", processed, err, test.wantErr)
			}
			if got := handler.calls.Load(); got != 1 {
				t.Fatalf("handler calls = %d, want 1", got)
			}
			if got := repo.applyCalls.Load(); got != test.wantApplyCalls {
				t.Fatalf("apply calls = %d, want %d", got, test.wantApplyCalls)
			}
			if got := repo.releaseCalls.Load(); got != 0 {
				t.Fatalf("started batch released %d claims, want leases left for recovery", got)
			}
		})
	}
}

func TestResetBatchRetryStreakPreservesConcurrentCapabilityPause(t *testing.T) {
	service := newExecutionBatchTestService(
		&executionBatchTestRepo{},
		&executionBatchTestFailedRepo{},
		&executionBatchTestTransactor{},
	)
	capability := JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion}
	pauseUntil := time.Now().UTC().Add(time.Minute)
	service.batchStateMu.Lock()
	service.batchStreak[capability] = 3
	service.batchPaused[capability] = pauseUntil
	service.batchStateMu.Unlock()

	service.resetBatchRetryStreak(capability)

	if !service.batchCapabilityPaused(capability, time.Now().UTC()) {
		t.Fatal("resetting one completed batch cleared another batch's future capability pause")
	}
	service.batchStateMu.Lock()
	defer service.batchStateMu.Unlock()
	if _, ok := service.batchStreak[capability]; ok {
		t.Fatal("top-level retry streak was not reset")
	}
	if got := service.batchPaused[capability]; !got.Equal(pauseUntil) {
		t.Fatalf("capability pause = %v, want %v", got, pauseUntil)
	}
}

func TestExecutionBatchCancellationLeavesClaimForRecovery(t *testing.T) {
	started := make(chan struct{})
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
	}
	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(ctx context.Context, items []BatchJobItem) (BatchResult, error) {
			close(started)
			<-ctx.Done()
			return successfulExecutionBatchResult(items), nil
		},
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1, MaxBytes: 1024, MaxWait: time.Millisecond})

	runCtx, cancelRun := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := service.findAndProcessExecutionBatch(
			runCtx,
			logger.Discard(),
			JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
		)
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("batch handler did not start")
	}
	cancelRun()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("find and process batch error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled batch did not return")
	}

	if got := repo.applyCalls.Load(); got != 0 {
		t.Fatalf("cancelled batch finalized %d times, want 0", got)
	}
	if got := repo.releaseCalls.Load(); got != 0 {
		t.Fatalf("started batch released %d claims, want the lease left for recovery", got)
	}
}

func TestExecutionBatchAdmissionRejectsDrain(t *testing.T) {
	repo := &executionBatchTestRepo{}
	failed := &executionBatchTestFailedRepo{}
	tx := &executionBatchTestTransactor{}
	service := newExecutionBatchTestService(repo, failed, tx)
	handler := &executionBatchTestHandler{name: testBatchJobName}
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1, MaxBytes: 1024, MaxWait: time.Millisecond})
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		service.drainDo.Do(func() { close(service.drain) })
		return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
	}

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if !processed || !errors.Is(err, ErrServiceDraining) {
		t.Fatalf("find and process batch = (%v, %v), want (true, %v)", processed, err, ErrServiceDraining)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("draining service entered handler %d times, want 0", got)
	}
	if got := repo.releaseCalls.Load(); got != 1 {
		t.Fatalf("draining service released %d claims, want 1", got)
	}
}

func TestExecutionBatchClaimsShareDrainBoundary(t *testing.T) {
	claimEntered := make(chan struct{})
	unblockClaim := make(chan struct{})
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		close(claimEntered)
		<-unblockClaim
		return []models.Job{executionBatchTestJob(testBatchJobName, leaseToken)}, nil
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	token := types.NewLeaseToken()
	claimDone := make(chan error, 1)
	go func() {
		_, err := service.claimExecutionBatchSingletonWithToken(
			t.Context(),
			repo,
			JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
			token,
		)
		claimDone <- err
	}()

	<-claimEntered
	drainDone := make(chan struct{})
	go func() {
		service.BeginDrain()
		close(drainDone)
	}()
	select {
	case <-drainDone:
		t.Fatal("BeginDrain returned while an execution-batch claim was still in progress")
	case <-time.After(20 * time.Millisecond):
	}
	close(unblockClaim)
	if err := <-claimDone; err != nil {
		t.Fatalf("in-flight claim failed: %v", err)
	}
	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("BeginDrain did not finish after the claim boundary was released")
	}

	_, err := service.claimExecutionBatchSingletonWithToken(
		t.Context(),
		repo,
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
		types.NewLeaseToken(),
	)
	if !errors.Is(err, ErrServiceDraining) {
		t.Fatalf("claim after BeginDrain error = %v, want %v", err, ErrServiceDraining)
	}
}

func TestWorkerScheduleAlternatesBatchAndSingleAndRotatesBatchCapabilities(t *testing.T) {
	var sequenceMu sync.Mutex
	sequence := make([]string, 0, 3)
	record := func(name string) {
		sequenceMu.Lock()
		sequence = append(sequence, name)
		sequenceMu.Unlock()
	}
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		capability JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		return []models.Job{executionBatchTestJob(capability.Name, leaseToken)}, nil
	}
	repo.findSingle = func(
		_ context.Context,
		leaseToken LeaseToken,
		capabilities []JobCapability,
	) ([]models.Job, error) {
		return []models.Job{executionBatchTestJob(capabilities[0].Name, leaseToken)}, nil
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(&executionBatchTestHandler{name: testBatchAJobName, after: record}, BatchConfig{MaxMessages: 1})
	service.MustRegisterBatchJob(&executionBatchTestHandler{name: testBatchBJobName, after: record}, BatchConfig{MaxMessages: 1})
	service.MustRegisterJob(&executionSingleTestHandler{name: testSingleJobName, after: record})

	batchCapabilities := []JobCapability{
		{Name: testBatchAJobName, SchemaVersion: DefaultSchemaVersion},
		{Name: testBatchBJobName, SchemaVersion: DefaultSchemaVersion},
	}
	singleCapabilities := []JobCapability{{Name: testSingleJobName, SchemaVersion: DefaultSchemaVersion}}
	schedule := workerSchedule{preferBatch: true}
	for range 3 {
		didWork, err := service.processWorkerWork(
			t.Context(),
			logger.Discard(),
			singleCapabilities,
			batchCapabilities,
			&schedule,
		)
		if err != nil || !didWork {
			t.Fatalf("process worker work = (%v, %v), want work without error", didWork, err)
		}
	}

	sequenceMu.Lock()
	defer sequenceMu.Unlock()
	want := []string{testBatchAJobName, testSingleJobName, testBatchBJobName}
	if len(sequence) != len(want) {
		t.Fatalf("handler sequence = %v, want %v", sequence, want)
	}
	for index := range want {
		if sequence[index] != want[index] {
			t.Fatalf("handler sequence = %v, want %v", sequence, want)
		}
	}
}

func TestExecutionBatchDLQUsesConfiguredRepositoryAndTransaction(t *testing.T) {
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(
		_ context.Context,
		_ JobCapability,
		leaseToken LeaseToken,
		_ int,
	) ([]models.Job, error) {
		job := executionBatchTestJob(testBatchJobName, leaseToken)
		job.Payload = `{"event":"payload"}`
		return []models.Job{job}, nil
	}
	failed := &executionBatchTestFailedRepo{}
	tx := &executionBatchTestTransactor{}
	service := newExecutionBatchTestService(repo, failed, tx)
	service.MustRegisterBatchJob(&executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
			return BatchResult{Items: []BatchItemResult{{
				JobID: items[0].JobID,
				Err:   Permanent(errors.New("invalid payload")),
			}}}, nil
		},
	}, BatchConfig{MaxMessages: 1})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if err != nil || !processed {
		t.Fatalf("find and process batch = (%v, %v), want successful processing", processed, err)
	}
	if got := tx.calls.Load(); got != 1 {
		t.Fatalf("batch finalization transaction calls = %d, want 1", got)
	}
	failed.mu.Lock()
	defer failed.mu.Unlock()
	if len(failed.jobs) != 1 {
		t.Fatalf("configured failed repository received %d jobs, want 1", len(failed.jobs))
	}
	if failed.jobs[0].Payload != `{"event":"payload"}` {
		t.Fatalf("failed payload = %q", failed.jobs[0].Payload)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.outcomes) != 1 || len(repo.outcomes[0]) != 1 || repo.outcomes[0][0].Kind != BatchJobOutcomeDLQ {
		t.Fatalf("active repository outcomes = %#v, want one DLQ deletion", repo.outcomes)
	}
}

func TestExecutionBatchFinalizationDeadlineScalesWithDLQItems(t *testing.T) {
	const batchSize = MaxReservationBatchSize

	leaseToken := types.NewLeaseToken()
	jobs := make([]models.Job, batchSize)
	outcomes := make([]BatchJobOutcome, batchSize)
	for index := range batchSize {
		jobs[index] = executionBatchTestJob(testBatchJobName, leaseToken)
		jobs[index].ReservedAt.Time = time.Now().UTC().Add(time.Second)
		outcomes[index] = dlqExecutionBatchOutcome(jobs[index], "permanent failure")
	}

	var leaseUntil time.Time
	repo := &executionBatchTestRepo{
		onExtendLease: func(_ []types.JobID, _, until time.Time) {
			leaseUntil = until
		},
	}
	var transactionDeadline time.Time
	tx := &executionBatchTestTransactor{
		onRun: func(ctx context.Context) {
			transactionDeadline, _ = ctx.Deadline()
		},
	}
	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, tx)
	batchCtx, cancelBatch := context.WithCancelCause(t.Context())
	manager := newBatchLeaseManager(
		batchCtx,
		repo,
		jobs,
		leaseToken,
		time.Hour,
		cancelBatch,
	)
	startedAt := time.Now()

	err := service.applyExecutionBatchOutcomes(t.Context(), repo, manager, jobs, outcomes)
	cancelBatch(nil)
	if heartbeatErr := manager.stopAndWait(); heartbeatErr != nil {
		t.Fatalf("stop batch heartbeat: %v", heartbeatErr)
	}
	if err != nil {
		t.Fatalf("apply full DLQ batch: %v", err)
	}

	wantTimeout := leaseFinalizationTimeout + batchSize*batchDLQInsertAllowance
	if got := transactionDeadline.Sub(startedAt); got < wantTimeout-time.Second {
		t.Fatalf("finalization deadline = %v, want approximately %v", got, wantTimeout)
	}
	if leaseUntil.Before(transactionDeadline.Add(batchFinalizationMargin - 100*time.Millisecond)) {
		t.Fatalf("finalization lease until = %v, want after transaction deadline %v", leaseUntil, transactionDeadline)
	}
}

type executionBatchTestRepo struct {
	mu            sync.Mutex
	findBatch     func(context.Context, JobCapability, LeaseToken, int) ([]models.Job, error)
	findSingle    func(context.Context, LeaseToken, []JobCapability) ([]models.Job, error)
	onExtend      func([]types.JobID)
	onExtendLease func([]types.JobID, time.Time, time.Time)
	onRelease     func([]types.JobID)
	outcomes      [][]BatchJobOutcome
	applyErr      error
	applyCalls    atomic.Int32
	releaseCalls  atomic.Int32
	deleteCalls   atomic.Int32
}

type boundedExecutionBatchTestRepo struct {
	*executionBatchTestRepo
	findBounded func(context.Context, JobCapability, LeaseToken, BatchClaimLimits) ([]models.Job, error)
}

func (r *boundedExecutionBatchTestRepo) FindAndReserveJobsForCapabilityBounded(
	ctx context.Context,
	_ time.Time,
	_ time.Time,
	leaseToken LeaseToken,
	capability JobCapability,
	limits BatchClaimLimits,
) ([]models.Job, error) {
	if r.findBounded == nil {
		return nil, ErrNoJobs
	}
	return r.findBounded(ctx, capability, leaseToken, limits)
}

func (*executionBatchTestRepo) CreateJobVersioned(
	context.Context,
	string,
	SchemaVersion,
	string,
	time.Time,
) (types.JobID, error) {
	return types.NewJobID(), nil
}

func (r *executionBatchTestRepo) FindAndReserveJobsForCapabilities(
	ctx context.Context,
	_ time.Time,
	_ time.Time,
	leaseToken LeaseToken,
	capabilities []JobCapability,
	_ int,
) ([]models.Job, error) {
	if r.findSingle == nil {
		return nil, ErrNoJobs
	}
	return r.findSingle(ctx, leaseToken, capabilities)
}

func (r *executionBatchTestRepo) FindAndReserveJobsForCapability(
	ctx context.Context,
	_ time.Time,
	_ time.Time,
	leaseToken LeaseToken,
	capability JobCapability,
	limit int,
) ([]models.Job, error) {
	if r.findBatch == nil {
		return nil, ErrNoJobs
	}
	return r.findBatch(ctx, capability, leaseToken, limit)
}

func (r *executionBatchTestRepo) ExtendJobLeases(
	_ context.Context,
	jobIDs []types.JobID,
	_ LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	if r.onExtend != nil {
		r.onExtend(jobIDs)
	}
	if r.onExtendLease != nil {
		r.onExtendLease(jobIDs, now, until)
	}
	return int64(len(jobIDs)), nil
}

func (r *executionBatchTestRepo) ReleaseUnstartedJobsWithLease(
	_ context.Context,
	jobIDs []types.JobID,
	_ LeaseToken,
	_ time.Time,
) (int64, error) {
	r.releaseCalls.Add(1)
	if r.onRelease != nil {
		r.onRelease(jobIDs)
	}
	return int64(len(jobIDs)), nil
}

func (r *executionBatchTestRepo) DeleteJobWithLease(
	context.Context,
	types.JobID,
	LeaseToken,
	time.Time,
) (int64, error) {
	r.deleteCalls.Add(1)
	return 1, nil
}

func (*executionBatchTestRepo) RescheduleJobWithLease(
	context.Context,
	types.JobID,
	LeaseToken,
	time.Time,
	time.Time,
) (int64, error) {
	return 1, nil
}

func (*executionBatchTestRepo) DeferJobWithLease(
	context.Context,
	types.JobID,
	LeaseToken,
	time.Time,
	time.Time,
) (int64, error) {
	return 1, nil
}

func (*executionBatchTestRepo) MaxReservationBatchSize() int { return MaxReservationBatchSize }

func (*executionBatchTestRepo) MaxExecutionBatchSize() int { return MaxReservationBatchSize }

func (r *executionBatchTestRepo) ApplyBatchJobOutcomes(
	_ context.Context,
	_ LeaseToken,
	_ time.Time,
	outcomes []BatchJobOutcome,
) (int64, error) {
	r.applyCalls.Add(1)
	if r.applyErr != nil {
		return 0, r.applyErr
	}
	r.mu.Lock()
	r.outcomes = append(r.outcomes, append([]BatchJobOutcome(nil), outcomes...))
	r.mu.Unlock()
	return int64(len(outcomes)), nil
}

type executionBatchTestFailedRepo struct {
	mu   sync.Mutex
	jobs []models.JobFailed
}

func (r *executionBatchTestFailedRepo) CreateFailedJobVersioned(
	_ context.Context,
	jobID types.JobID,
	name string,
	schemaVersion SchemaVersion,
	payload string,
	reason string,
) (types.JobID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := types.NewJobID()
	r.jobs = append(r.jobs, models.JobFailed{
		ID:            id,
		JobID:         jobID,
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		Reason:        reason,
	})
	return id, nil
}

type executionBatchTestTransactor struct {
	calls atomic.Int32
	onRun func(context.Context)
}

func (t *executionBatchTestTransactor) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	t.calls.Add(1)
	if t.onRun != nil {
		t.onRun(ctx)
	}
	return fn(ctx)
}

func (t *executionBatchTestTransactor) SupportsAtomicDLQ() bool {
	return true
}

type executionBatchTestHandler struct {
	name    string
	timeout time.Duration
	handle  func(context.Context, []BatchJobItem) (BatchResult, error)
	after   func(string)
	calls   atomic.Int32
}

func (h *executionBatchTestHandler) Name() string { return h.name }

func (h *executionBatchTestHandler) HandleBatch(
	ctx context.Context,
	items []BatchJobItem,
) (BatchResult, error) {
	h.calls.Add(1)
	if h.after != nil {
		h.after(h.name)
	}
	if h.handle != nil {
		return h.handle(ctx, items)
	}
	return successfulExecutionBatchResult(items), nil
}

func (h *executionBatchTestHandler) ExecutionTimeout() time.Duration {
	if h.timeout > 0 {
		return h.timeout
	}
	return time.Hour
}

func (*executionBatchTestHandler) MaxAttempts() int { return 3 }

type executionSingleTestHandler struct {
	name  string
	after func(string)
}

func (h *executionSingleTestHandler) Name() string { return h.name }

func (h *executionSingleTestHandler) Handle(context.Context, string) error {
	if h.after != nil {
		h.after(h.name)
	}
	return nil
}

func (*executionSingleTestHandler) ExecutionTimeout() time.Duration { return time.Hour }

func (*executionSingleTestHandler) MaxAttempts() int { return 3 }

func newExecutionBatchTestService(
	repo JobsRepository,
	failed JobsFailedRepository,
	transactor Transactor,
) *Service {
	return &Service{
		Options: Options{
			workers:              1,
			idleTime:             100 * time.Millisecond,
			reserveFor:           time.Hour,
			reservationBatchSize: 1,
			jobsRepo:             repo,
			jobsFailedRepo:       failed,
			transactor:           transactor,
			logger:               logger.Discard(),
		},
		jobs:        make(map[JobCapability]Job),
		batchJobs:   make(map[JobCapability]batchJobRegistration),
		drain:       make(chan struct{}),
		batchPaused: make(map[JobCapability]time.Time),
		batchStreak: make(map[JobCapability]int),
	}
}

func executionBatchTestJob(name string, leaseToken LeaseToken) models.Job {
	return models.Job{
		ID:            types.NewJobID(),
		Name:          name,
		SchemaVersion: DefaultSchemaVersion,
		Payload:       `{}`,
		Attempts:      1,
		ReservedAt: sql.NullTime{
			Time:  time.Now().UTC().Add(time.Hour),
			Valid: true,
		},
		LeaseToken: leaseToken,
	}
}

func successfulExecutionBatchResult(items []BatchJobItem) BatchResult {
	result := BatchResult{Items: make([]BatchItemResult, len(items))}
	for index, item := range items {
		result.Items[index] = BatchItemResult{JobID: item.JobID}
	}
	return result
}

func waitForExtendedJobIDs(t *testing.T, calls <-chan []types.JobID, want ...types.JobID) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case jobIDs := <-calls:
			seen := make(map[types.JobID]struct{}, len(jobIDs))
			for _, jobID := range jobIDs {
				seen[jobID] = struct{}{}
			}
			allPresent := true
			for _, jobID := range want {
				if _, ok := seen[jobID]; !ok {
					allPresent = false
					break
				}
			}
			if allPresent {
				return
			}
		case <-timer.C:
			t.Fatalf("heartbeat did not cover job IDs %v while tail release was blocked", want)
		}
	}
}

func TestExecutionBatchHandlerPanicDoesNotApplyOutcomesAndFailsClosed(t *testing.T) {
	repo := &executionBatchTestRepo{}
	repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
		job := executionBatchTestJob(testBatchJobName, token)
		job.Attempts = 2
		return []models.Job{job}, nil
	}

	handler := &executionBatchTestHandler{
		name: testBatchJobName,
		handle: func(_ context.Context, _ []BatchJobItem) (BatchResult, error) {
			panic("deterministic test panic in batch handler")
		},
	}

	service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
	service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1, MaxWait: time.Millisecond})

	processed, err := service.findAndProcessExecutionBatch(
		t.Context(),
		logger.Discard(),
		JobCapability{Name: testBatchJobName, SchemaVersion: DefaultSchemaVersion},
	)
	if !processed {
		t.Fatalf("expected batch to be processed, got %v", processed)
	}
	if err == nil {
		t.Fatal("expected panic error from findAndProcessExecutionBatch, got nil")
	}

	var panicErr *HandlerPanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("expected HandlerPanicError, got: %T (%v)", err, err)
	}
	if panicErr.JobName != testBatchJobName {
		t.Fatalf("expected job name %q, got %q", testBatchJobName, panicErr.JobName)
	}
	if panicErr.Value != "deterministic test panic in batch handler" {
		t.Fatalf("unexpected panic value: %v", panicErr.Value)
	}
	if len(panicErr.Stack) == 0 {
		t.Fatal("expected non-empty stack trace")
	}

	// ApplyBatchJobOutcomes must NOT have been called (no deferral, no attempts reduction)
	if calls := repo.applyCalls.Load(); calls != 0 {
		t.Fatalf("ApplyBatchJobOutcomes called %d times, want 0", calls)
	}

	// ReleaseUnstarted must NOT release the panicked job (manager.forgetAll was called)
	if calls := repo.releaseCalls.Load(); calls != 0 {
		t.Fatalf("ReleaseUnstartedJobsWithLease called %d times, want 0", calls)
	}
}
