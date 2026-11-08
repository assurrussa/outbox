package transaction_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
)

const (
	modeSuccess      = "success"
	modeError        = "error"
	modePanic        = "panic"
	modeGoexit       = "goexit"
	modeNestedGoexit = "nested goexit"
)

type auditTx struct {
	commits, rollbacks     int
	commitErr, rollbackErr error
}

func (tx *auditTx) Commit() error   { tx.commits++; return tx.commitErr }
func (tx *auditTx) Rollback() error { tx.rollbacks++; return tx.rollbackErr }

type auditConnector struct{ tx *auditTx }

func (c auditConnector) Connect(context.Context) (driver.Conn, error) { return auditConn(c), nil }
func (c auditConnector) Driver() driver.Driver                        { return auditDriver(c) }

type auditDriver struct{ tx *auditTx }

func (d auditDriver) Open(string) (driver.Conn, error) { return auditConn(d), nil }

type auditConn struct{ tx *auditTx }

func (c auditConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (c auditConn) Close() error                        { return nil }
func (c auditConn) Begin() (driver.Tx, error)           { return c.tx, nil }

func (c auditConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.tx, nil }

func TestTransactionCallbackExit(t *testing.T) {
	for _, mode := range []string{modeSuccess, modeError, modePanic, modeGoexit, modeNestedGoexit} {
		t.Run(mode, func(t *testing.T) {
			tx := &auditTx{}
			db := sql.OpenDB(auditConnector{tx})
			defer db.Close()
			manager := transaction.New(db)
			done := make(chan struct{})
			var err error
			callbackErr := errors.New("callback failed")
			go func() {
				defer close(done)
				err = manager.RunInTx(context.Background(), func(ctx context.Context) error {
					switch mode {
					case modeError:
						return callbackErr
					case modePanic:
						panic("callback panicked")
					case modeGoexit:
						runtime.Goexit()
					case modeNestedGoexit:
						return manager.RunInTx(ctx, func(context.Context) error { runtime.Goexit(); return nil })
					}
					return nil
				})
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("transaction callback did not unwind")
			}
			assertCallbackExitOutcome(t, mode, tx, err, callbackErr)
		})
	}
}

func assertCallbackExitOutcome(t *testing.T, mode string, tx *auditTx, err, callbackErr error) {
	t.Helper()
	if mode == modeSuccess {
		if err != nil || tx.commits != 1 || tx.rollbacks != 0 {
			t.Fatalf("success: err=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
		}
		return
	}
	if tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("aborted callback committed: commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
	}
	if mode == modeError && !errors.Is(err, callbackErr) {
		t.Fatalf("callback error lost: %v", err)
	}
	if (mode == modeError || mode == modePanic) && err == nil {
		t.Fatal("missing callback error")
	}
}

func TestBorrowedTransactionRemainsCallerOwned(t *testing.T) {
	driverTx := &auditTx{}
	db := sql.OpenDB(auditConnector{driverTx})
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	callbackErr := errors.New("borrowed callback failed")
	txCtx := transaction.WithTx(context.Background(), tx)
	err = transaction.New(db).RunInTx(txCtx, func(context.Context) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error lost: %v", err)
	}
	if driverTx.commits != 0 || driverTx.rollbacks != 0 {
		t.Fatalf("borrowed tx finalized: %+v", driverTx)
	}
}

func TestTransactionFinalizationErrors(t *testing.T) {
	primaryErr := errors.New("primary failure")
	rollbackErr := errors.New("rollback failure")
	for _, commitFailure := range []bool{false, true} {
		tx := &auditTx{rollbackErr: rollbackErr}
		if commitFailure {
			tx.commitErr = primaryErr
		}
		db := sql.OpenDB(auditConnector{tx})
		err := transaction.New(db).RunInTx(context.Background(), func(context.Context) error {
			if commitFailure {
				return nil
			}
			return primaryErr
		})
		db.Close()
		if !errors.Is(err, primaryErr) {
			t.Fatalf("primary failure lost: %v", err)
		}
		if commitFailure {
			// database/sql marks Tx done even when the driver's Commit fails.
			// An additional Rollback cannot reach the driver or disambiguate commit.
			if tx.commits != 1 || tx.rollbacks != 0 {
				t.Fatalf("commit failure cleanup: %+v", tx)
			}
		} else if !errors.Is(err, rollbackErr) || tx.rollbacks != 1 {
			t.Fatalf("rollback failure lost: %v", err)
		}
	}
}
