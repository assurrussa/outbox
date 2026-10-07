package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnectorRejectsUnconfiguredConnection(t *testing.T) {
	const (
		unexpectedJournal = "unexpected journal"
		offFileJournal    = "OFF file journal"
		memoryFileJournal = "MEMORY file journal"
	)
	configurationErr := errors.New("owned configuration failure")
	closeErr := errors.New("owned close failure")
	for _, failure := range []string{
		unexpectedJournal, offFileJournal, memoryFileJournal, "journal query failure", "configuration and close failure",
	} {
		t.Run(failure, func(t *testing.T) {
			conn := &connectorTestConn{journal: "wal"}
			switch failure {
			case unexpectedJournal:
				conn.journal = "delete"
			case offFileJournal:
				conn.journal = "off"
			case memoryFileJournal:
				conn.journal = "memory"
			case "journal query failure":
				conn.queryErr = configurationErr
			case "configuration and close failure":
				conn.execErr, conn.closeErr = configurationErr, closeErr
			}
			connector := &sqliteConnector{driver: &connectorTestDriver{conn: conn}, synchronous: SynchronousNormal}
			opened, err := connector.Connect(t.Context())
			require.Nil(t, opened)
			require.Error(t, err)
			require.True(t, conn.closed)
			switch failure {
			case unexpectedJournal:
				require.ErrorContains(t, err, `effective journal mode is "delete"`)
			case offFileJournal:
				require.ErrorContains(t, err, `effective journal mode is "off"`)
			case memoryFileJournal:
				require.ErrorContains(t, err, `effective journal mode is "memory"`)
			default:
				require.ErrorIs(t, err, configurationErr)
			}
			if conn.closeErr != nil {
				require.ErrorIs(t, err, closeErr)
			}
			for _, rows := range conn.results {
				require.Equal(t, 1, rows.closeCalls, "raw driver results close exactly once")
			}
		})
	}
}

func TestConnectorHonorsCancellationBeforeOpen(t *testing.T) {
	driver := &connectorTestDriver{}
	connector := &sqliteConnector{driver: driver}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	conn, err := connector.Connect(ctx)
	require.Nil(t, conn)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, driver.opened)
}

type connectorTestDriver struct {
	conn   *connectorTestConn
	opened bool
}

func (d *connectorTestDriver) Open(string) (driver.Conn, error) { d.opened = true; return d.conn, nil }

type connectorTestConn struct {
	results                     []*connectorTestRows
	journal                     string
	execErr, queryErr, closeErr error
	closed                      bool
}

func (*connectorTestConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*connectorTestConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *connectorTestConn) Close() error {
	c.closed = true
	return c.closeErr
}

func (c *connectorTestConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), c.execErr
}

func (c *connectorTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	values := []driver.Value{c.journal}
	if query == "PRAGMA database_list;" {
		values = []driver.Value{int64(0), "main", "owned-file.db"}
	}
	rows := &connectorTestRows{values: values}
	c.results = append(c.results, rows)
	return rows, nil
}

type connectorTestRows struct {
	closeCalls int
	values     []driver.Value
	done       bool
}

func (*connectorTestRows) Columns() []string { return []string{"journal_mode"} }
func (r *connectorTestRows) Close() error {
	r.closeCalls++
	return nil
}

func (r *connectorTestRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(values, r.values)
	r.done = true
	return nil
}
