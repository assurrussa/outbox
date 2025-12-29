package utilst

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func CreateTmpDirectory(t *testing.T, pattern string) (string, func()) {
	t.Helper()

	tmpDirectory, err := os.MkdirTemp("", pattern)
	require.NoError(t, err)

	return tmpDirectory, func() {
		assert.NoError(t, os.RemoveAll(tmpDirectory))
	}
}

func CopyDirectory(t *testing.T, src, dst string) {
	t.Helper()

	src, _ = strings.CutSuffix(src, "/")
	dst, _ = strings.CutSuffix(dst, "/")

	files, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, f := range files {
		if f.IsDir() {
			continue
		}

		CopyFile(t, src+"/"+f.Name(), dst+"/"+f.Name())
	}
}
