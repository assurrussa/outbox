package transaction

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"runtime"
	"testing"
	"time"
)

type auditTx struct {
	commits, rollbacks     int
	commitErr, rollbackErr error
}

func (tx *auditTx) Commit() error   { tx.commits++; return tx.commitErr }
func (tx *auditTx) Rollback() error { tx.rollbacks++; return tx.rollbackErr }

type auditConnector struct{ tx *auditTx }

func (c auditConnector) Connect(context.Context) (driver.Conn, error) { return auditConn{c.tx}, nil }
func (c auditConnector) Driver() driver.Driver                        { return auditDriver{c.tx} }

type auditDriver struct{ tx *auditTx }

func (d auditDriver) Open(string) (driver.Conn, error) { return auditConn{d.tx}, nil }

type auditConn struct{ tx *auditTx }

func (c auditConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (c auditConn) Close() error                        { return nil }
func (c auditConn) Begin() (driver.Tx, error)           { return c.tx, nil }

func (c auditConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.tx, nil }

func TestTransactionCallbackExit(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "goexit", "nested goexit"} {
		t.Run(mode, func(t *testing.T) {
			tx := &auditTx{}
			db := sql.OpenDB(auditConnector{tx})
			defer db.Close()
			manager := New(db)
			done := make(chan struct{})
			var err error
			callbackErr := errors.New("callback failed")
			go func() {
				defer close(done)
				err = manager.RunInTx(context.Background(), func(ctx context.Context) error {
					switch mode {
					case "error":
						return callbackErr
					case "panic":
						panic("callback panicked")
					case "goexit":
						runtime.Goexit()
					case "nested goexit":
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
			if mode == "success" {
				if err != nil || tx.commits != 1 || tx.rollbacks != 0 {
					t.Fatalf("success: err=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
				}
			} else {
				if tx.commits != 0 || tx.rollbacks != 1 {
					t.Fatalf("aborted callback committed: commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
				}
				if mode == "error" && !errors.Is(err, callbackErr) {
					t.Fatalf("callback error lost: %v", err)
				}
				if (mode == "error" || mode == "panic") && err == nil {
					t.Fatal("missing callback error")
				}
			}
		})
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
	defer tx.Rollback()
	callbackErr := errors.New("borrowed callback failed")
	err = New(db).RunInTx(WithTx(context.Background(), tx), func(context.Context) error { return callbackErr })
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
		err := New(db).RunInTx(context.Background(), func(context.Context) error {
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
