package testload_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/shared/loadenv"
	"github.com/assurrussa/outbox/shared/tools"
)

var runRateLimitCh = make(chan struct{}, 1)

func TestLoad(t *testing.T) {
	t.Parallel()

	runRateLimitCh <- struct{}{}
	defer func() {
		<-runRateLimitCh
	}()

	require.NoError(t, os.Unsetenv("APP_TESTDATA_CHECK"))
	require.NoError(t, os.Unsetenv("ENV_OVERRIDE"))
	callerFile := tools.CallerCurrentFile()
	assert.Empty(t, os.Getenv("APP_TESTDATA_CHECK"))
	loadenv.Load(callerFile)
	assert.Equal(t, "checktest", os.Getenv("APP_TESTDATA_CHECK"))
	require.NoError(t, os.Setenv("ENV_OVERRIDE", "1"))
	loadenv.Load(callerFile)
	assert.Equal(t, "checktest_local", os.Getenv("APP_TESTDATA_CHECK"))
}
