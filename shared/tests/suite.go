package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type OptionsSuite struct {
	Timeout    time.Duration
	IsParallel bool
}

type OptionSuite func(*OptionsSuite)

func WithTimeout(timeout time.Duration) OptionSuite {
	return func(suite *OptionsSuite) {
		suite.Timeout = timeout
	}
}

func WithIsParallel(val bool) OptionSuite {
	return func(suite *OptionsSuite) {
		suite.IsParallel = val
	}
}

type suiteTest interface {
	SetT(t *testing.T)
}

// The NewSuite method creates a suite that can be run on different cases and run parallel tests.
func NewSuite[T suiteTest](
	t *testing.T,
	creator func(*testing.T, context.Context) T,
	opts ...OptionSuite,
) (context.Context, context.CancelFunc, T) {
	t.Helper()

	return NewSuiteWithContext(t, context.Background(), creator, opts...)
}

// The NewSuiteWithContext method creates a suite that can be run on different cases and run parallel tests.
func NewSuiteWithContext[T suiteTest](
	t *testing.T,
	ctx context.Context,
	creator func(*testing.T, context.Context) T,
	opts ...OptionSuite,
) (context.Context, context.CancelFunc, T) {
	t.Helper()

	options := &OptionsSuite{
		IsParallel: true,
	}

	for _, o := range opts {
		o(options)
	}

	if options.IsParallel {
		t.Parallel()
	}

	ctx, cancelCtx := context.WithCancel(ctx)
	if options.Timeout > 0 {
		ctx, cancelCtx = context.WithTimeout(ctx, options.Timeout)
	}

	t.Cleanup(func() {
		t.Helper()
		cancelCtx()
	})

	ts := creator(t, ctx)
	ts.SetT(t)

	if v, ok := any(ts).(suite.SetupAllSuite); ok {
		v.SetupSuite()
	}

	if v, ok := any(ts).(suite.SetupTestSuite); ok {
		v.SetupTest()
	}

	if v, ok := any(ts).(suite.TearDownAllSuite); ok {
		t.Cleanup(func() {
			v.TearDownSuite()
		})
	}

	if v, ok := any(ts).(suite.TearDownTestSuite); ok {
		t.Cleanup(func() {
			v.TearDownTest()
		})
	}

	return ctx, cancelCtx, ts
}
