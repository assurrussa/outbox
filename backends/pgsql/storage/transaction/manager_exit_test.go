package transaction_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
)

const (
	modeSuccess      = "success"
	modeError        = "error"
	modePanic        = "panic"
	modeGoexit       = "goexit"
	modeNestedGoexit = "nested goexit"
	modeCancel       = "cancel"
	modeCommitError  = "commit error"
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
	modes := []string{
		modeSuccess,
		modeError,
		modePanic,
		modeGoexit,
		modeNestedGoexit,
		modeCancel,
		modeCommitError,
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			tx := &auditTx{}
			manager := transaction.New(auditDB{tx})
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), auditContextKey{}, "retained"))
			defer cancel()
			callbackErr := errors.New("callback failed")
			if mode == modeCommitError {
				tx.commitErr = errors.New("commit failed")
			}
			done := make(chan struct{})
			var err error
			go func() {
				defer close(done)
				err = manager.RunInTx(ctx, func(ctx context.Context) error {
					switch mode {
					case modeError:
						return callbackErr
					case modePanic:
						panic("callback panicked")
					case modeGoexit:
						runtime.Goexit()
					case modeNestedGoexit:
						return manager.RunInTx(ctx, func(context.Context) error { runtime.Goexit(); return nil })
					case modeCancel:
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
			assertCallbackExitOutcome(t, mode, tx, err, callbackErr)
		})
	}
}

func assertCallbackExitOutcome(t *testing.T, mode string, tx *auditTx, err, callbackErr error) {
	t.Helper()
	wantCommits := 0
	if mode == modeSuccess || mode == modeCommitError {
		wantCommits = 1
	}
	if tx.commits != wantCommits {
		t.Fatalf("commits=%d want=%d", tx.commits, wantCommits)
	}
	if mode == modeSuccess {
		if err != nil || tx.rollbacks != 0 {
			t.Fatalf("success: err=%v rollbacks=%d", err, tx.rollbacks)
		}
		return
	}
	if tx.rollbacks != 1 {
		t.Fatalf("rollbacks=%d want=1", tx.rollbacks)
	}
	if tx.rollbackContextError != nil || !tx.rollbackBounded || tx.rollbackValue != "retained" {
		t.Fatalf("cleanup context: err=%v bounded=%v value=%v", tx.rollbackContextError, tx.rollbackBounded, tx.rollbackValue)
	}
	if mode == modeCancel && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if mode == modeCommitError && !errors.Is(err, tx.commitErr) {
		t.Fatalf("commit error lost: %v", err)
	}
	if mode == modeError && !errors.Is(err, callbackErr) {
		t.Fatalf("callback error lost: %v", err)
	}
}

func TestBorrowedTransactionRemainsCallerOwned(t *testing.T) {
	tx := &auditTx{}
	callbackErr := errors.New("borrowed callback failed")
	txCtx := pgsql.WithTx(context.Background(), tx)
	err := transaction.New(auditDB{tx}).RunInTx(txCtx, func(context.Context) error { return callbackErr })
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
		err := transaction.New(auditDB{tx}).RunInTx(context.Background(), func(context.Context) error {
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
