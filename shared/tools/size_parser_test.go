package tools_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/shared/tools"
)

func TestParseSize_WithBytes(t *testing.T) {
	t.Parallel()

	size, err := tools.ParseSize("20B")
	require.NoError(t, err)
	require.Equal(t, 20, size)

	size, err = tools.ParseSize("20b")
	require.NoError(t, err)
	require.Equal(t, 20, size)

	size, err = tools.ParseSize("20")
	require.NoError(t, err)
	require.Equal(t, 20, size)
}

func TestParseSize_WithKiloBytes(t *testing.T) {
	t.Parallel()

	size, err := tools.ParseSize("20KB")
	require.NoError(t, err)
	require.Equal(t, 20*1024, size)

	size, err = tools.ParseSize("20Kb")
	require.NoError(t, err)
	require.Equal(t, 20*1024, size)

	size, err = tools.ParseSize("20kb")
	require.NoError(t, err)
	require.Equal(t, 20*1024, size)
}

func TestParseSize_WithMegaBytes(t *testing.T) {
	t.Parallel()

	size, err := tools.ParseSize("20MB")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024, size)

	size, err = tools.ParseSize("20Mb")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024, size)

	size, err = tools.ParseSize("20mb")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024, size)
}

func TestParseSize_WithGigaBytes(t *testing.T) {
	t.Parallel()

	size, err := tools.ParseSize("20GB")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024*1024, size)

	size, err = tools.ParseSize("20Gb")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024*1024, size)

	size, err = tools.ParseSize("20gb")
	require.NoError(t, err)
	require.Equal(t, 20*1024*1024*1024, size)
}

func TestParseSize_Incorrect(t *testing.T) {
	t.Parallel()

	_, err := tools.ParseSize("-20")
	require.Error(t, err)

	_, err = tools.ParseSize("20   ")
	require.Error(t, err)

	_, err = tools.ParseSize("-20TB")
	require.Error(t, err)

	_, err = tools.ParseSize("abc")
	require.Error(t, err)
}
