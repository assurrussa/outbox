package tests_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDependencyIsolation_CoreDoesNotImportDBDrivers(t *testing.T) {
	mods := listPackageModules(t, ".", "github.com/assurrussa/outbox/outbox")

	require.NotContains(t, mods, "github.com/go-sql-driver/mysql")
	require.NotContains(t, mods, "modernc.org/sqlite")
	require.NotContains(t, mods, "github.com/jackc/pgx/v5")
	require.NotContains(t, mods, "github.com/picodata/picodata-go")
}

func TestDependencyIsolation_BackendModulesImportOnlyOwnDriver(t *testing.T) {
	testCases := []struct {
		name          string
		dir           string
		pkg           string
		shouldHave    string
		expectCore    bool
		shouldNotHave []string
	}{
		{
			name:       "mysql",
			dir:        filepath.Join("..", "..", "backends", "mysql"),
			pkg:        "github.com/assurrussa/outbox/backends/mysql/storage",
			shouldHave: "github.com/go-sql-driver/mysql",
			expectCore: true,
			shouldNotHave: []string{
				"modernc.org/sqlite",
				"github.com/picodata/picodata-go",
			},
		},
		{
			name:       "sqlite",
			dir:        filepath.Join("..", "..", "backends", "sqlite"),
			pkg:        "github.com/assurrussa/outbox/backends/sqlite/storage",
			shouldHave: "modernc.org/sqlite",
			expectCore: true,
			shouldNotHave: []string{
				"github.com/go-sql-driver/mysql",
				"github.com/picodata/picodata-go",
			},
		},
		{
			name:       "pgsql",
			dir:        filepath.Join("..", "..", "backends", "pgsql"),
			pkg:        "github.com/assurrussa/outbox/backends/pgsql/storage",
			shouldHave: "github.com/jackc/pgx/v5",
			expectCore: false,
			shouldNotHave: []string{
				"github.com/go-sql-driver/mysql",
				"modernc.org/sqlite",
				"github.com/picodata/picodata-go",
			},
		},
		{
			name:       "picodata",
			dir:        filepath.Join("..", "..", "backends", "picodata"),
			pkg:        "github.com/assurrussa/outbox/backends/picodata/storage",
			shouldHave: "github.com/picodata/picodata-go",
			expectCore: true,
			shouldNotHave: []string{
				"github.com/go-sql-driver/mysql",
				"modernc.org/sqlite",
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			mods := listPackageModules(t, tt.dir, tt.pkg)

			require.Contains(t, mods, tt.shouldHave)
			for _, mod := range tt.shouldNotHave {
				require.NotContains(t, mods, mod)
			}
			if tt.expectCore {
				require.Contains(t, mods, "github.com/assurrussa/outbox")
			}
		})
	}
}

func listPackageModules(t *testing.T, dir, pkg string) map[string]struct{} {
	t.Helper()

	cmd := exec.CommandContext(
		t.Context(),
		"go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}",
		pkg,
	)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.Output()
	require.NoError(t, err)

	result := make(map[string]struct{})
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		modulePath := strings.TrimSpace(line)
		if modulePath == "" {
			continue
		}
		result[modulePath] = struct{}{}
	}

	return result
}
