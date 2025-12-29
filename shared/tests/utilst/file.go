package utilst

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func FindBasePath() (string, error) {
	basePath := os.Getenv("BASE_PATH")
	if basePath != "" {
		return basePath, nil
	}

	currentDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for {
		goModPath := filepath.Join(currentDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return currentDir, nil
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return "", errors.New("go.mod not found")
		}

		currentDir = parentDir
	}
}

func CopyFile(t *testing.T, src, dst string) {
	t.Helper()

	sourceFile, err := os.Open(src)
	require.NoError(t, err, "copyFile os.Open")
	defer func() {
		assert.NoError(t, sourceFile.Close())
	}()

	destFile, err := os.Create(dst)
	require.NoError(t, err, "copyFile os.Create")
	defer func() {
		assert.NoError(t, destFile.Close())
	}()

	_, err = io.Copy(destFile, sourceFile)
	require.NoError(t, err, "copyFile io.Copy")

	err = destFile.Sync()
	require.NoError(t, err, "copyFile destFile.Sync")
}
