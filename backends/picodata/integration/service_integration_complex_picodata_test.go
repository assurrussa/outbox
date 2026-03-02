//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// TestComplex apologizes for its content.
func TestPicodataComplex(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const (
		jobSuccessfulFromFirstTime = "successful-from-first-time-job" // job1
		jobSuccessfulFromThirdTime = "successful-from-third-time-job" // job2

		jobFailedAfterSecondTime = "failed-after-second-time-job" // job3
		jobFailedAfterFiveTime   = "failed-after-five-time-job"   // job4

		jobTmpTimeoutedAndSuccessfulAfter = "temporary-timeouted-and-successful-after-job" // job5
		jobTmpTimeoutedAndFailedAfter     = "temporary-timeouted-and-failed-after-job"     // job6

		jobUnknown = "unknown-job"
	)

	executedTimes := newJobInstancesExecutedTimes()

	job1 := newJobMock(jobSuccessfulFromFirstTime, nop, time.Second, 10)
	ts.outboxSvc.MustRegisterJob(job1)

	job2 := newJobMock(jobSuccessfulFromThirdTime, func(ctx context.Context, payloadAsIndex string) error {
		k := jobSuccessfulFromThirdTime + payloadAsIndex
		if executedTimes.Inc(k) == 3 {
			return nil
		}
		return errors.New("sorry I'm failed")
	}, time.Second, 4)
	ts.outboxSvc.MustRegisterJob(job2)

	job3 := newJobMock(jobFailedAfterSecondTime, func(ctx context.Context, _ string) error {
		return errors.New("sorry I'm failed")
	}, time.Second, 2)
	ts.outboxSvc.MustRegisterJob(job3)

	job4 := newJobMock(jobFailedAfterFiveTime, func(ctx context.Context, _ string) error {
		return errors.New("sorry I'm failed")
	}, time.Second, 5)
	ts.outboxSvc.MustRegisterJob(job4)

	job5 := newJobMock(jobTmpTimeoutedAndSuccessfulAfter, func(ctx context.Context, payloadAsIndex string) error {
		k := jobTmpTimeoutedAndSuccessfulAfter + payloadAsIndex
		if executedTimes.Inc(k) == 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		return nil
	}, time.Millisecond, 2)
	ts.outboxSvc.MustRegisterJob(job5)

	job6 := newJobMock(jobTmpTimeoutedAndFailedAfter, func(ctx context.Context, payloadAsIndex string) error {
		k := jobTmpTimeoutedAndFailedAfter + payloadAsIndex
		if executedTimes.Inc(k) == 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		return errors.New("sorry I'm failed")
	}, time.Millisecond, 2)
	ts.outboxSvc.MustRegisterJob(job6)

	// Action.
	cancel, errCh := runPicodataOutbox(ctx, ts)
	defer cancel()

	jobCounts := map[string]int{
		jobSuccessfulFromFirstTime: 50,
		jobSuccessfulFromThirdTime: 4,

		jobFailedAfterSecondTime: 1,
		jobFailedAfterFiveTime:   2,

		jobTmpTimeoutedAndSuccessfulAfter: 3,
		jobTmpTimeoutedAndFailedAfter:     3,

		jobUnknown: 30,
	}

	wg, ctx := errgroup.WithContext(ctx)

	for jobName, jobCount := range jobCounts {
		jobName, jobCount := jobName, jobCount
		wg.Go(func() error {
			for i := 1; i <= jobCount; i++ {
				var randAvailableAt time.Time
				// Random choice between immediate and delayed job.
				if rand.Float64() >= 0.5 { //nolint:gosec
					randAvailableAt = time.Now()
				} else {
					randAvailableAt = time.Now().Add(4 * idleTime)
				}

				if _, err := ts.outboxSvc.Put(ctx, jobName, strconv.Itoa(i), randAvailableAt); err != nil {
					return err
				}
			}
			return nil
		})
	}
	err := wg.Wait()
	ts.Require().NoError(err)

	// Assert.
	time.Sleep(10 * time.Second)

	cancel()
	ts.Require().NoError(<-errCh)

	{
		ts.Equal(jobCounts[jobSuccessfulFromFirstTime]*1, job1.ExecutedTimes())
		ts.Equal(jobCounts[jobSuccessfulFromThirdTime]*3, job2.ExecutedTimes())

		ts.Equal(jobCounts[jobFailedAfterSecondTime]*2, job3.ExecutedTimes())
		ts.Equal(jobCounts[jobFailedAfterFiveTime]*5, job4.ExecutedTimes())

		ts.Equal(jobCounts[jobTmpTimeoutedAndSuccessfulAfter]*2, job5.ExecutedTimes())
		ts.Equal(jobCounts[jobTmpTimeoutedAndFailedAfter]*2, job6.ExecutedTimes())
	}
	{
		count, err := ts.jobsRepo.CountExact(context.WithoutCancel(ctx))
		ts.Require().NoError(err)
		ts.Equal(int64(0), count)

		failedJobsTotal := jobCounts[jobFailedAfterSecondTime] +
			jobCounts[jobFailedAfterFiveTime] +
			jobCounts[jobTmpTimeoutedAndFailedAfter] +
			jobCounts[jobUnknown]
		count, err = ts.jobsFailedRepo.CountExact(context.WithoutCancel(ctx))
		ts.Require().NoError(err)
		ts.Equal(int64(failedJobsTotal), count)
	}
}
