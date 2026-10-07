package storage_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/storage"
	sqlitetx "github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
)

func TestConnectionConfiguration(t *testing.T) {
	for _, state := range []string{"second physical connection", "zero idle connections", "discarded connection replacement"} {
		t.Run(state, func(t *testing.T) {
			options := []storage.Option{storage.WithMaxOpenConns(2)}
			if state == "zero idle connections" {
				options = append(options, storage.WithMaxIdleConns(0))
			}
			client, err := storage.Create(t.Context(), filepath.Join(t.TempDir(), "connections.db"), options...)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			first, err := client.DB().Conn(t.Context())
			require.NoError(t, err)
			if state == "discarded connection replacement" {
				require.ErrorIs(t, first.Raw(func(any) error { return driver.ErrBadConn }), driver.ErrBadConn)
				err = first.Close()
				require.True(t, err == nil || errors.Is(err, sql.ErrConnDone))
			} else {
				t.Cleanup(func() { require.NoError(t, first.Close()) })
			}
			conn, err := client.DB().Conn(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, conn.Close()) }()
			assertConnectionSettings(t, conn, 1)
		})
	}
}

func TestSynchronousModeConfigurationAndDSNPrecedence(t *testing.T) {
	for _, mode := range []storage.SynchronousMode{"", storage.SynchronousNormal, storage.SynchronousFull} {
		t.Run(string(mode), func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "settings.db") + "?_pragma=synchronous(OFF)" +
				"&_pragma=foreign_keys(OFF)&_pragma=busy_timeout(7)&_pragma=journal_mode(DELETE)&_pragma=cache_size(-321)"
			client, err := storage.Create(t.Context(), dsn, storage.WithSynchronousMode(mode))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			first, err := client.DB().Conn(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, first.Close()) }()
			second, err := client.DB().Conn(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, second.Close()) }()
			want := 1
			if mode == storage.SynchronousFull {
				want = 2
			}
			for _, conn := range []*sql.Conn{first, second} {
				assertConnectionSettings(t, conn, want)
				var cacheSize int
				require.NoError(t, conn.QueryRowContext(t.Context(), "PRAGMA cache_size").Scan(&cacheSize))
				require.Equal(t, -321, cacheSize, "unmanaged DSN settings remain intact")
			}
		})
	}
}

func TestSynchronousModeValidation(t *testing.T) {
	for _, mode := range []storage.SynchronousMode{"OFF", "EXTRA", "normal", "FULL; SELECT 1"} {
		_, err := storage.Create(t.Context(), filepath.Join(t.TempDir(), "invalid.db"), storage.WithSynchronousMode(mode))
		require.ErrorContains(t, err, "synchronous mode must be NORMAL or FULL")
	}
}

func TestMemoryJournalCompatibility(t *testing.T) {
	for _, dsn := range []string{
		":memory:", "file:memory-journal?mode=memory&cache=shared", ":memory:?_pragma=journal_mode(OFF)",
	} {
		t.Run(dsn, func(t *testing.T) {
			client, err := storage.Create(t.Context(), dsn,
				storage.WithMaxOpenConns(1), storage.WithSynchronousMode(storage.SynchronousFull))
			require.NoError(t, err)
			defer func() { require.NoError(t, client.Close()) }()
			var mode string
			require.NoError(t, client.DB().QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode))
			want := "memory"
			if dsn == ":memory:?_pragma=journal_mode(OFF)" {
				want = "off"
			}
			require.Equal(t, want, mode)
			_, err = client.DB().ExecContext(t.Context(), "CREATE TABLE compatible (id INTEGER PRIMARY KEY)")
			require.NoError(t, err)
		})
	}
}

func TestRejectsFileBackedMemoryJournal(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "memory-journal.db") +
		"?vfs=unix-dotfile&_pragma=journal_mode(MEMORY)"
	// Confirm the pinned driver's VFS retains MEMORY when WAL is requested.
	// The backing file distinguishes this from a legitimate memory database.
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var mode string
	err = db.QueryRowContext(t.Context(), "PRAGMA journal_mode=WAL").Scan(&mode)
	if err != nil && strings.Contains(err.Error(), "no such vfs") {
		t.Skip("pinned SQLite driver has no unix-dotfile VFS on this platform")
	}
	require.NoError(t, err)
	require.Equal(t, "memory", mode)
	require.NoError(t, db.Close())
	client, err := storage.Create(t.Context(), dsn)
	if client != nil {
		t.Cleanup(func() { require.NoError(t, client.Close()) })
	}
	require.Nil(t, client)
	require.ErrorContains(t, err, `effective journal mode is "memory"`)
}

func TestSynchronousModeReopenAndRollback(t *testing.T) {
	for _, mode := range []storage.SynchronousMode{storage.SynchronousNormal, storage.SynchronousFull} {
		t.Run(string(mode), func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "reopen.db")
			client, err := storage.Create(t.Context(), dsn, storage.WithSynchronousMode(mode))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			_, err = client.DB().ExecContext(t.Context(), "CREATE TABLE records (id INTEGER PRIMARY KEY)")
			require.NoError(t, err)
			manager := sqlitetx.New(client.DB())
			write := func(ctx context.Context) error {
				_, err := sqlitetx.GetTx(ctx).ExecContext(ctx, "INSERT INTO records DEFAULT VALUES")
				return err
			}
			require.NoError(t, manager.RunInTx(t.Context(), write))
			rollback := errors.New("roll back owned write")
			err = manager.RunInTx(t.Context(), func(ctx context.Context) error {
				if err := write(ctx); err != nil {
					return err
				}
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			require.NoError(t, client.Close())
			reopened, err := storage.Create(t.Context(), dsn, storage.WithSynchronousMode(mode))
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			var count int
			require.NoError(t, reopened.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM records").Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func assertConnectionSettings(t *testing.T, conn *sql.Conn, synchronous int) {
	t.Helper()
	for pragma, expected := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000, "synchronous": synchronous} {
		var actual int
		require.NoError(t, conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&actual))
		assert.Equal(t, expected, actual, pragma)
	}
	var journal string
	require.NoError(t, conn.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal))
	assert.Equal(t, "wal", journal)
}
