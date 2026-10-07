package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
)

type sqliteConnector struct {
	driver      driver.Driver
	dsn         string
	synchronous SynchronousMode
}

func (c *sqliteConnector) Driver() driver.Driver { return c.driver }

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if err := configureSQLite(ctx, conn, c.synchronous); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

type pragmaConnection interface {
	driver.ExecerContext
	driver.QueryerContext
}

func configureSQLite(ctx context.Context, conn driver.Conn, mode SynchronousMode) error {
	pragma, ok := conn.(pragmaConnection)
	if !ok {
		return errors.New("SQLite driver does not support context-aware configuration")
	}
	// The driver applies DSN options before this backend-owned configuration.
	// Establish lock waiting before attempting to change the journal mode.
	if _, err := pragma.ExecContext(ctx, "PRAGMA busy_timeout=5000;", nil); err != nil {
		return fmt.Errorf("SQLite busy timeout: %w", err)
	}
	if err := configureJournal(ctx, pragma); err != nil {
		return err
	}
	for _, query := range []string{"PRAGMA foreign_keys=ON;", "PRAGMA synchronous=" + string(mode) + ";"} {
		if _, err := pragma.ExecContext(ctx, query, nil); err != nil {
			return fmt.Errorf("SQLite configuration %q: %w", query, err)
		}
	}
	return ctx.Err()
}

func configureJournal(ctx context.Context, conn pragmaConnection) error {
	mode, err := readJournalMode(ctx, conn)
	if err != nil {
		return err
	}
	if mode == "wal" {
		return nil
	}
	// MEMORY and OFF are also valid for files when a VFS cannot use WAL.
	// Preserve these legitimate memory-DB modes only when SQLite confirms
	// there is no main database file; mode alone does not establish that.
	if mode == "memory" || mode == "off" {
		memory, err := hasNoMainDatabaseFile(ctx, conn)
		if err != nil {
			return err
		}
		if memory {
			return nil
		}
	}
	return fmt.Errorf("SQLite requested WAL but effective journal mode is %q", mode)
}

func readJournalMode(ctx context.Context, conn pragmaConnection) (mode string, resultErr error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA journal_mode=WAL;", nil)
	if err != nil {
		return "", fmt.Errorf("SQLite journal mode: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		return "", fmt.Errorf("read SQLite journal mode: %w", err)
	}
	return fmt.Sprint(values[0]), nil
}

func hasNoMainDatabaseFile(ctx context.Context, conn pragmaConnection) (memory bool, resultErr error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA database_list;", nil)
	if err != nil {
		return false, fmt.Errorf("read SQLite database location: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	values := make([]driver.Value, 3)
	for {
		if err := rows.Next(values); err != nil {
			if errors.Is(err, io.EOF) {
				return false, errors.New("SQLite main database location is missing")
			}
			return false, fmt.Errorf("read SQLite database location: %w", err)
		}
		if fmt.Sprint(values[1]) == "main" {
			return fmt.Sprint(values[2]) == "", nil
		}
	}
}
