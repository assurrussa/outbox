//go:build integration

package outbox_test

import (
	"context"
	"sync/atomic"
	"time"
)

var (
	workers    = 10
	idleTime   = 250 * time.Millisecond
	reserveFor = time.Second
)

var nop = func(_ context.Context, _ string) error {
	time.Sleep(10 * time.Millisecond)
	return nil
}

type jobMock struct {
	name          string
	handler       func(ctx context.Context, s string) error
	timeout       time.Duration
	maxAttempts   int
	executedTimes int32
}

func newJobMock(
	name string,
	h func(ctx context.Context, s string) error,
	executionTimeout time.Duration,
	maxAttempts int,
) *jobMock {
	return &jobMock{
		name:          name,
		handler:       h,
		timeout:       executionTimeout,
		maxAttempts:   maxAttempts,
		executedTimes: 0,
	}
}

func (j *jobMock) Name() string {
	return j.name
}

func (j *jobMock) Handle(ctx context.Context, payload string) error {
	atomic.AddInt32(&j.executedTimes, 1)
	return j.handler(ctx, payload)
}

func (j *jobMock) ExecutionTimeout() time.Duration {
	return j.timeout
}

func (j *jobMock) MaxAttempts() int {
	return j.maxAttempts
}

func (j *jobMock) ExecutedTimes() int {
	return int(atomic.LoadInt32(&j.executedTimes))
}
