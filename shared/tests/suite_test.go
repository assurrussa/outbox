package tests_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/shared/tests"
)

type testSuite struct {
	suite.Suite

	testVal int64
}

type hookSuite struct {
	suite.Suite
	hooks *[]string
}

func (s *hookSuite) SetupSuite() {
	*s.hooks = append(*s.hooks, "setup_suite")
}

func (s *hookSuite) SetupTest() {
	*s.hooks = append(*s.hooks, "setup_test")
}

func (s *hookSuite) TearDownTest() {
	*s.hooks = append(*s.hooks, "teardown_test")
}

func (s *hookSuite) TearDownSuite() {
	*s.hooks = append(*s.hooks, "teardown_suite")
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

func TestNewSuite_TearDownHooksRunOnCleanup(t *testing.T) {
	hooks := make([]string, 0, 4)

	t.Run("suite", func(t *testing.T) {
		_, cancel, _ := tests.NewSuite[*hookSuite](t, func(t *testing.T, _ context.Context) *hookSuite {
			t.Helper()
			return &hookSuite{hooks: &hooks}
		}, tests.WithIsParallel(false))
		require.NotNil(t, cancel)
		require.Equal(t, []string{"setup_suite", "setup_test"}, hooks)
	})

	require.Equal(
		t,
		[]string{"setup_suite", "setup_test", "teardown_test", "teardown_suite"},
		hooks,
	)
}
