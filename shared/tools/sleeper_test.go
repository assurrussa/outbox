package tools_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/assurrussa/outbox/shared/tools"
)

func TestRunSleeper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var finished atomic.Bool
	go func() {
		tools.RunSleeper(ctx, 20*time.Millisecond)
		finished.Store(true)
	}()
	assert.Eventually(t, finished.Load, 1*time.Second, 10*time.Millisecond)

	ctx.Done()
}

func TestRunSleeper_CtxCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	var finished atomic.Bool
	go func() {
		tools.RunSleeper(ctx, 5*time.Second)
		finished.Store(true)
	}()
	assert.Eventually(t, finished.Load, 1*time.Second, 10*time.Millisecond)

	ctx.Done()
}
