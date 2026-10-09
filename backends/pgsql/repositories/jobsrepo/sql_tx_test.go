package jobsrepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coreoutbox "github.com/assurrussa/outbox/outbox"
)

func TestSQLTxPutterRequiresTransaction(t *testing.T) {
	putter, err := NewSQLTxPutter(nil)
	require.Error(t, err)
	require.Nil(t, putter)

	for _, invalid := range []*SQLTxPutter{nil, {}} {
		result, err := invalid.PutVersionedUnique(t.Context(), "key", "event", 1, "payload", time.Now())
		require.Error(t, err)
		require.Zero(t, result)
	}
}

func TestSQLTxPutterUsesOnlyCallerTransaction(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			tx, state := newSQLTxTestTransaction(t)
			putter, err := NewSQLTxPutter(tx)
			require.NoError(t, err)
			require.Zero(t, state.queries)

			at := time.Date(2026, 10, 9, 12, 0, 0, 123456000, time.FixedZone("offset", 3600))
			result, err := putter.PutVersionedUnique(t.Context(), "identity", "event.created", 2, "exact payload", at)
			require.NoError(t, err)
			require.True(t, result.Created)
			require.False(t, result.JobID.IsZero())
			require.Equal(t, 1, state.connections)
			require.Equal(t, 1, state.begins)
			require.Equal(t, 1, state.queries)
			require.Equal(t, 1, state.rowsClosed)
			require.Zero(t, state.commits)
			require.Zero(t, state.rollbacks)
			require.Contains(t, state.query, "with key_row as")
			require.Len(t, state.args, 9)
			require.Equal(t, "identity", state.args[0].Value)
			require.Equal(t, result.JobID.String(), state.args[1].Value)
			require.Equal(t, jobFingerprint("event.created", 2, "exact payload", at), state.args[2].Value)
			require.Equal(t, "event.created", state.args[4].Value)
			require.Equal(t, int64(2), state.args[5].Value)
			require.Equal(t, "exact payload", state.args[6].Value)
			require.Equal(t, at, state.args[7].Value)
			require.Equal(t, "00000000-0000-0000-0000-000000000000", state.args[8].Value)

			if commit {
				require.NoError(t, tx.Commit())
				require.Equal(t, 1, state.commits)
				require.Zero(t, state.rollbacks)
			} else {
				require.NoError(t, tx.Rollback())
				require.Zero(t, state.commits)
				require.Equal(t, 1, state.rollbacks)
			}
			ended, err := putter.PutVersionedUnique(t.Context(), "next", "event.created", 2, "payload", at)
			require.ErrorIs(t, err, sql.ErrTxDone)
			require.Zero(t, ended)
			require.Equal(t, 1, state.queries)
			require.Equal(t, 1, state.connections)
			require.Equal(t, 1, state.begins)
		})
	}
}

func TestSQLTxPutterValidatesBeforeQuery(t *testing.T) {
	for _, test := range []struct {
		name, key, capability string
		version               coreoutbox.SchemaVersion
	}{
		{name: "empty key", capability: "event", version: 1},
		{name: "empty capability", key: "key", version: 1},
		{name: "zero version", key: "key", capability: "event"},
		{name: "negative version", key: "key", capability: "event", version: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, state := newSQLTxTestTransaction(t)
			putter, err := NewSQLTxPutter(tx)
			require.NoError(t, err)
			result, err := putter.PutVersionedUnique(t.Context(), test.key, test.capability, test.version, "payload", time.Now())
			require.Error(t, err)
			require.Zero(t, result)
			require.Zero(t, state.queries)
			require.Zero(t, state.commits)
			require.Zero(t, state.rollbacks)
		})
	}
}

func TestSQLTxPutterPreservesErrors(t *testing.T) {
	queryErr := errors.New("driver rejected PostgreSQL query")
	scanErr := errors.New("driver could not read result")
	for _, test := range []struct {
		name     string
		queryErr error
		scanErr  error
		noRows   bool
		want     error
	}{
		{name: "wrong driver or query failure", queryErr: queryErr, want: queryErr},
		{name: "scan failure", scanErr: scanErr, want: scanErr},
		{name: "identity conflict", noRows: true, want: coreoutbox.ErrIdempotencyConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, state := newSQLTxTestTransaction(t)
			state.queryErr, state.scanErr, state.noRows = test.queryErr, test.scanErr, test.noRows
			putter, err := NewSQLTxPutter(tx)
			require.NoError(t, err)
			result, err := putter.PutVersionedUnique(t.Context(), "key", "event", 1, "payload", time.Now())
			require.ErrorIs(t, err, test.want)
			require.Zero(t, result)
			require.Equal(t, 1, state.queries)
			require.Equal(t, 1, state.connections)
			require.Equal(t, 1, state.begins)
			require.Zero(t, state.commits)
			require.Zero(t, state.rollbacks)
		})
	}
}

func TestSQLTxPutterHonorsContext(t *testing.T) {
	tx, state := newSQLTxTestTransaction(t)
	putter, err := NewSQLTxPutter(tx)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := putter.PutVersionedUnique(ctx, "key", "event", 1, "payload", time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, result)
	require.Zero(t, state.queries)
	require.Zero(t, state.commits)
	require.Zero(t, state.rollbacks)
}

type sqlTxTestState struct {
	connections, begins, queries, commits, rollbacks, rowsClosed int
	query                                                       string
	args                                                        []driver.NamedValue
	queryErr, scanErr                                           error
	noRows                                                      bool
}

func newSQLTxTestTransaction(t *testing.T) (*sql.Tx, *sqlTxTestState) {
	t.Helper()
	state := &sqlTxTestState{}
	db := sql.OpenDB(sqlTxTestConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx, state
}

type sqlTxTestConnector struct{ state *sqlTxTestState }

func (c sqlTxTestConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.connections++
	return sqlTxTestConn{state: c.state}, nil
}

func (sqlTxTestConnector) Driver() driver.Driver { return sqlTxTestDriver{} }

type sqlTxTestDriver struct{}

func (sqlTxTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected driver.Open")
}

type sqlTxTestConn struct{ state *sqlTxTestState }

func (sqlTxTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}

func (sqlTxTestConn) Close() error { return nil }

func (c sqlTxTestConn) Begin() (driver.Tx, error) {
	c.state.begins++
	return sqlTxTestTransaction{state: c.state}, nil
}

func (c sqlTxTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.queries++
	c.state.query = query
	c.state.args = append([]driver.NamedValue(nil), args...)
	if c.state.queryErr != nil {
		return nil, c.state.queryErr
	}
	return &sqlTxTestRows{state: c.state, id: args[1].Value}, nil
}

type sqlTxTestTransaction struct{ state *sqlTxTestState }

func (tx sqlTxTestTransaction) Commit() error {
	tx.state.commits++
	return nil
}

func (tx sqlTxTestTransaction) Rollback() error {
	tx.state.rollbacks++
	return nil
}

type sqlTxTestRows struct {
	state *sqlTxTestState
	id    driver.Value
	read  bool
}

func (*sqlTxTestRows) Columns() []string { return []string{"job_id"} }

func (r *sqlTxTestRows) Close() error {
	r.state.rowsClosed++
	return nil
}

func (r *sqlTxTestRows) Next(values []driver.Value) error {
	if r.state.scanErr != nil {
		return r.state.scanErr
	}
	if r.read || r.state.noRows {
		return io.EOF
	}
	r.read = true
	values[0] = r.id
	return nil
}
