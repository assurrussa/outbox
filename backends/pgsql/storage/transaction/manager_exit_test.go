package transaction

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/jackc/pgx/v5"
)

type auditTx struct {
	pgx.Tx
	commits, rollbacks     int
	rollbackContextError   error
	rollbackBounded        bool
	rollbackValue          any
	commitErr, rollbackErr error
}
type auditContextKey struct{}

func (tx *auditTx) Commit(context.Context) error { tx.commits++; return tx.commitErr }
func (tx *auditTx) Rollback(ctx context.Context) error {
	tx.rollbacks++
	tx.rollbackContextError = ctx.Err()
	deadline, ok := ctx.Deadline()
	tx.rollbackBounded = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second
	tx.rollbackValue = ctx.Value(auditContextKey{})
	return tx.rollbackErr
}

type auditDB struct{ tx *auditTx }

func (db auditDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return db.tx, nil }

func TestTransactionCallbackExit(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "goexit", "nested goexit", "cancel", "commit error"} {
		t.Run(mode, func(t *testing.T) {
			tx := &auditTx{}
			manager := New(auditDB{tx})
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), auditContextKey{}, "retained"))
			defer cancel()
			callbackErr := errors.New("callback failed")
			if mode == "commit error" {
				tx.commitErr = errors.New("commit failed")
			}
			done := make(chan struct{})
			var err error
			go func() {
				defer close(done)
				err = manager.RunInTx(ctx, func(ctx context.Context) error {
					switch mode {
					case "error":
						return callbackErr
					case "panic":
						panic("callback panicked")
					case "goexit":
						runtime.Goexit()
					case "nested goexit":
						return manager.RunInTx(ctx, func(context.Context) error { runtime.Goexit(); return nil })
					case "cancel":
						cancel()
						return context.Canceled
					}
					return nil
				})
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("transaction callback did not unwind")
			}
			wantCommits := 0
			if mode == "success" || mode == "commit error" {
				wantCommits = 1
			}
			if tx.commits != wantCommits {
				t.Fatalf("commits=%d want=%d", tx.commits, wantCommits)
			}
			if mode == "success" {
				if err != nil || tx.rollbacks != 0 {
					t.Fatalf("success: err=%v rollbacks=%d", err, tx.rollbacks)
				}
			} else {
				if tx.rollbacks != 1 {
					t.Fatalf("rollbacks=%d want=1", tx.rollbacks)
				}
				if tx.rollbackContextError != nil || !tx.rollbackBounded || tx.rollbackValue != "retained" {
					t.Fatalf("cleanup context: err=%v bounded=%v value=%v", tx.rollbackContextError, tx.rollbackBounded, tx.rollbackValue)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				if mode == "commit error" && !errors.Is(err, tx.commitErr) {
					t.Fatalf("commit error lost: %v", err)
				}
				if mode == "error" && !errors.Is(err, callbackErr) {
					t.Fatalf("callback error lost: %v", err)
				}
			}
		})
	}
}

func TestBorrowedTransactionRemainsCallerOwned(t *testing.T) {
	tx := &auditTx{}
	callbackErr := errors.New("borrowed callback failed")
	err := New(auditDB{tx}).RunInTx(pgsql.WithTx(context.Background(), tx), func(context.Context) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error lost: %v", err)
	}
	if tx.commits != 0 || tx.rollbacks != 0 {
		t.Fatalf("borrowed tx finalized: %+v", tx)
	}
}

func TestTransactionPreservesCleanupAndCommitErrors(t *testing.T) {
	primaryErr := errors.New("primary error")
	rollbackErr := errors.New("rollback error")
	for _, commitFailure := range []bool{false, true} {
		tx := &auditTx{rollbackErr: rollbackErr}
		if commitFailure {
			tx.commitErr = primaryErr
		}
		err := New(auditDB{tx}).RunInTx(context.Background(), func(context.Context) error {
			if commitFailure {
				return nil
			}
			return primaryErr
		})
		if !errors.Is(err, primaryErr) || !errors.Is(err, rollbackErr) {
			t.Fatalf("errors not retained: %v", err)
		}
	}
}
