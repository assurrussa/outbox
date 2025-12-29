package tests_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/shared/tests"
)

type testSuite struct {
	suite.Suite

	testVal int64
}

func TestNewSuite_Base(t *testing.T) {
	ctx, cancel, ts := tests.NewSuite[*testSuite](t, func(t *testing.T, _ context.Context) *testSuite {
		t.Helper()
		return &testSuite{testVal: 1}
	})
	ts.NotNil(ctx)
	ts.NotNil(cancel)
}

func TestNewSuite_WithOption(t *testing.T) {
	ctx, cancel, ts := tests.NewSuite[*testSuite](t, func(t *testing.T, _ context.Context) *testSuite {
		t.Helper()
		return &testSuite{testVal: 1}
	}, tests.WithTimeout(time.Millisecond), tests.WithIsParallel(true))

	ts.NotNil(ctx)
	ts.NotNil(cancel)
}

func TestNewSuiteWithContext_WithOption(t *testing.T) {
	ctx, cancel, ts := tests.NewSuiteWithContext[*testSuite](t, context.Background(),
		func(t *testing.T, _ context.Context) *testSuite {
			t.Helper()
			return &testSuite{testVal: 1}
		}, tests.WithTimeout(time.Millisecond))

	ts.NotNil(ctx)
	ts.NotNil(cancel)
}

func TestNewSuite_Panic(t *testing.T) {
	assert.Panics(t, func() {
		tests.NewSuite[*testSuite](t, nil)
	})
}
