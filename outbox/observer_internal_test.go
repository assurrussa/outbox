package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

type observerMutationRepo struct {
	*executionBatchTestRepo
	affected    int64
	mutationErr error
	extendErr   error
	completed   bool
	before      func()
}

func (r *observerMutationRepo) mutate() (int64, error) {
	if r.before != nil {
		r.before()
	}
	if r.mutationErr != nil {
		return 0, r.mutationErr
	}
	r.completed = r.affected == 1
	return r.affected, nil
}

func (r *observerMutationRepo) DeleteJobWithLease(context.Context, JobID, LeaseToken, time.Time) (int64, error) {
	return r.mutate()
}

func (r *observerMutationRepo) RescheduleJobWithLease(context.Context, JobID, LeaseToken, time.Time, time.Time) (int64, error) {
	return r.mutate()
}

func (r *observerMutationRepo) DeferJobWithLease(context.Context, JobID, LeaseToken, time.Time, time.Time) (int64, error) {
	return r.mutate()
}

func (r *observerMutationRepo) ExtendJobLeases(
	ctx context.Context, ids []JobID, token LeaseToken, now, until time.Time,
) (int64, error) {
	if r.extendErr != nil {
		return 0, r.extendErr
	}
	return r.executionBatchTestRepo.ExtendJobLeases(ctx, ids, token, now, until)
}

type observerTestTransactor struct {
	commitErr     error
	confirmed     bool
	atomic        bool
	supportsCalls int
	beforeCommit  func()
}

func (t *observerTestTransactor) SupportsAtomicDLQ() bool { t.supportsCalls++; return t.atomic }
func (t *observerTestTransactor) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	if t.beforeCommit != nil {
		t.beforeCommit()
	}
	if t.commitErr != nil {
		return t.commitErr
	}
	t.confirmed = true
	return nil
}

const (
	observerCommitError  = "commit error"
	observerStorageError = "storage error"
	observerLostFence    = "lost fence"
	observerNonAtomic    = "non-atomic"
	observerConfirmed    = "confirmed"
)

func TestObserverSinglePersistenceBoundaries(t *testing.T) {
	for _, kind := range []RuntimeOutcomeKind{
		OutcomeACKConfirmed, OutcomeRetryPersisted, OutcomeDeferPersisted, OutcomeDLQCommitted,
	} {
		for _, failure := range []string{
			observerConfirmed, observerStorageError, observerLostFence, observerCommitError, observerNonAtomic,
		} {
			if (failure == observerCommitError || failure == observerNonAtomic) && kind != OutcomeDLQCommitted {
				continue
			}
			t.Run(string(kind)+"/"+failure, func(t *testing.T) { assertObserverSingleBoundary(t, kind, failure) })
		}
	}
}

func assertObserverSingleBoundary(t *testing.T, kind RuntimeOutcomeKind, failure string) {
	t.Helper()
	persistenceErr := errors.New("storage uncertain with private error text")
	at := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	events := make(chan RuntimeOutcome, 4)
	repo := &observerMutationRepo{executionBatchTestRepo: &executionBatchTestRepo{}, affected: 1}
	repo.before = func() { require.Empty(t, events, "success cannot precede persisted mutation") }
	tx := &observerTestTransactor{atomic: true, beforeCommit: func() { require.Empty(t, events, "DLQ cannot precede commit") }}
	switch failure {
	case observerStorageError:
		repo.mutationErr = persistenceErr
	case observerLostFence:
		repo.affected = 0
	case observerCommitError:
		tx.commitErr = persistenceErr
	case observerNonAtomic:
		tx.atomic = false
	}
	service := newObserverTestService(t, repo, tx, events)
	job := executionBatchTestJob(testSingleJobName, types.NewLeaseToken())
	job.SchemaVersion = 2
	job.Attempts = 3
	job.Payload = "private-payload-never-observed"
	var err error
	switch kind {
	case OutcomeACKConfirmed:
		err = service.ackBatch(t.Context(), repo, job)
	case OutcomeRetryPersisted:
		err = service.rescheduleLeased(t.Context(), job, at)
	case OutcomeDeferPersisted:
		err = service.deferLeased(t.Context(), job, at)
	case OutcomeDLQCommitted:
		err = service.dlqBatch(t.Context(), repo, job, "private-reason-never-observed")
	default:
		t.Fatal("unexpected outcome kind")
	}
	switch failure {
	case observerStorageError, observerCommitError:
		require.ErrorIs(t, err, persistenceErr)
	case observerLostFence:
		require.ErrorIs(t, err, ErrLeaseLost)
	default:
		require.NoError(t, err)
	}
	if failure != observerConfirmed {
		require.Empty(t, events)
		return
	}
	require.True(t, repo.completed)
	require.Len(t, events, 1)
	event := <-events
	require.Equal(t, kind, event.Kind)
	require.Equal(t, job.ID, event.JobID)
	require.Equal(t, JobCapability{Name: job.Name, SchemaVersion: 2}, event.Capability)
	require.Equal(t, job.Attempts, event.Attempt)
	require.False(t, event.ObservedAt.IsZero())
	require.Equal(t, time.UTC, event.ObservedAt.Location())
	if kind == OutcomeDLQCommitted {
		require.True(t, tx.confirmed)
		require.Equal(t, 1, tx.supportsCalls)
	}
	if kind == OutcomeRetryPersisted || kind == OutcomeDeferPersisted {
		require.Equal(t, at, event.AvailableAt)
	} else {
		require.True(t, event.AvailableAt.IsZero())
	}
}

func TestObserverBatchWaitsForTransactionResult(t *testing.T) {
	persistenceErr := errors.New("commit outcome uncertain")
	at := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, failure := range []string{observerConfirmed, "apply failure", "commit failure"} {
		t.Run(failure, func(t *testing.T) {
			events := make(chan RuntimeOutcome, 8)
			repo := &executionBatchTestRepo{}
			tx := &observerTestTransactor{atomic: true, beforeCommit: func() { require.Empty(t, events) }}
			if failure == "apply failure" {
				repo.applyErr = persistenceErr
			}
			if failure == "commit failure" {
				tx.commitErr = persistenceErr
			}
			service := newObserverTestService(t, repo, tx, events)
			token := types.NewLeaseToken()
			kinds := []BatchJobOutcomeKind{BatchJobOutcomeSuccess, BatchJobOutcomeRetry, BatchJobOutcomeDLQ, BatchJobOutcomeDefer}
			jobs := make([]models.Job, len(kinds))
			outcomes := make([]BatchJobOutcome, len(kinds))
			for index, kind := range kinds {
				jobs[index] = executionBatchTestJob(testBatchJobName, token)
				outcomes[index] = BatchJobOutcome{JobID: jobs[index].ID, Kind: kind, AvailableAt: at}
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			manager := newBatchLeaseManager(ctx, repo, jobs, token, time.Hour, cancel)
			defer func() { require.NoError(t, manager.stopAndWait()) }()
			err := service.applyExecutionBatchOutcomes(ctx, repo, manager, jobs, outcomes)
			if failure != observerConfirmed {
				require.ErrorIs(t, err, persistenceErr)
				require.Empty(t, events)
				return
			}
			require.NoError(t, err)
			require.True(t, tx.confirmed)
			require.Equal(t, 1, tx.supportsCalls)
			require.Len(t, events, len(kinds))
			want := []RuntimeOutcomeKind{OutcomeACKConfirmed, OutcomeRetryPersisted, OutcomeDLQCommitted, OutcomeDeferPersisted}
			for index, kind := range want {
				event := <-events
				require.Equal(t, kind, event.Kind)
				require.Equal(t, jobs[index].ID, event.JobID)
				require.Equal(t, 1, event.Attempt)
			}
		})
	}
}

func TestObserverLeaseLossAtReservationExit(t *testing.T) {
	for _, test := range []string{"single fence", "single heartbeat", "batch fence"} {
		t.Run(test, func(t *testing.T) {
			events := make(chan RuntimeOutcome, 4)
			base := &executionBatchTestRepo{}
			repo := &observerMutationRepo{executionBatchTestRepo: base, affected: 0}
			job := executionBatchTestJob(testSingleJobName, types.NewLeaseToken())
			repo.findSingle = func(_ context.Context, token LeaseToken, _ []JobCapability) ([]models.Job, error) {
				job.LeaseToken = token
				return []models.Job{job}, nil
			}
			repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
				job.LeaseToken = token
				return []models.Job{job}, nil
			}
			service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
			service.observer = events
			if test == "single heartbeat" {
				repo.extendErr = errors.New("heartbeat storage failure")
				job.ReservedAt.Time = time.Now().UTC().Add(time.Second)
			}
			var err error
			if test == "batch fence" {
				base.applyErr = ErrLeaseLost
				service.MustRegisterBatchJob(&executionBatchTestHandler{name: testSingleJobName}, BatchConfig{MaxMessages: 1})
				_, err = service.findAndProcessExecutionBatch(
					t.Context(), logger.Discard(), JobCapability{Name: testSingleJobName, SchemaVersion: 1},
				)
			} else {
				service.MustRegisterJob(&executionSingleTestHandler{name: testSingleJobName})
				err = service.findAndProcessBatch(t.Context(), logger.Discard(), []JobCapability{{Name: testSingleJobName, SchemaVersion: 1}})
			}
			require.ErrorIs(t, err, ErrLeaseLost)
			require.Len(t, events, 1)
			event := <-events
			require.Equal(t, OutcomeLeaseLost, event.Kind)
			require.Equal(t, 1, event.ClaimedJobs)
			require.True(t, event.JobID.IsZero())
			require.Zero(t, event.Capability)
			require.Zero(t, event.Attempt)
		})
	}
}

func TestObserverUnavailableSinkCannotAlterACK(t *testing.T) {
	for _, state := range []string{"nil", "unbuffered", "full", "closed"} {
		t.Run(state, func(t *testing.T) {
			var sink chan RuntimeOutcome
			switch state {
			case "unbuffered":
				sink = make(chan RuntimeOutcome)
			case "full":
				sink = make(chan RuntimeOutcome, 1)
				sink <- RuntimeOutcome{Kind: OutcomeLeaseLost}
			case "closed":
				sink = make(chan RuntimeOutcome)
				close(sink)
			}
			repo := &observerMutationRepo{executionBatchTestRepo: &executionBatchTestRepo{}, affected: 1}
			service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
			service.observer = sink
			require.NoError(t, service.ackBatch(t.Context(), repo, executionBatchTestJob(testSingleJobName, types.NewLeaseToken())))
			require.True(t, repo.completed)
		})
	}
}

func newObserverTestService(t *testing.T, repo JobsRepository, tx Transactor, events chan RuntimeOutcome) *Service {
	t.Helper()
	service, err := New(
		WithJobsRepo(repo), WithJobsFailedRepo(&executionBatchTestFailedRepo{}), WithTransactor(tx),
		WithAllowNonAtomicDLQ(), WithObserver(events), WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	return service
}
