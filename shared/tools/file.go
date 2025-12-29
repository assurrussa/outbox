package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

func CallerCurrentFile() string {
	_, currentFile, _, _ := runtime.Caller(0) //nolint:dogsled // because for test
	for i := 1; ; i++ {
		_, file, _, _ := runtime.Caller(i)
		if file != currentFile {
			currentFile = file
			break
		}
	}

	return currentFile
}

func FindFileDir(filePath string, callerFile string) string {
	dir := filepath.Dir(callerFile)
	gopath := filepath.Clean(os.Getenv("GOPATH"))
	visited := make(map[string]struct{}, 10)
	for dir != gopath {
		if _, ok := visited[dir]; ok {
			break
		}
		visited[dir] = struct{}{}
		path := filepath.Join(dir, filePath)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			dir = filepath.Dir(dir)
			continue
		}

		return path
	}

	return ""
}
