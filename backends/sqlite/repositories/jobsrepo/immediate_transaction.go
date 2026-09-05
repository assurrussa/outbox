package jobsrepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
)

const immediateRollbackTimeout = 5 * time.Second

type immediateConnection interface {
	sqlExecutor
	Raw(func(any) error) error
	Close() error
}

func (r *Repo) withImmediateTransaction(ctx context.Context, work func(sqlExecutor) error) error {
	if tx := transaction.GetTx(ctx); tx != nil {
		return work(tx)
	}
	conn, err := r.client.DB().Conn(ctx)
	if err != nil {
		return err
	}
	return runImmediateTransaction(ctx, conn, work)
}

// runImmediateTransaction owns conn until it is clean or discarded. A manual
// BEGIN is invisible to database/sql, so Conn.Close alone cannot clean it up.
func runImmediateTransaction(ctx context.Context, conn immediateConnection, work func(sqlExecutor) error) (resultErr error) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			resultErr = errors.Join(resultErr, fmt.Errorf("close immediate transaction connection: %w", err))
		}
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		// Cancellation can race a successful BEGIN inside the driver. Discard
		// even when it is unclear whether SQLite actually opened a transaction.
		return errors.Join(err, discardImmediateConnection(conn))
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), immediateRollbackTimeout)
		defer cancel()
		if _, err := conn.ExecContext(cleanupCtx, "ROLLBACK;"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback immediate transaction: %w", err), discardImmediateConnection(conn))
		}
	}()
	if err := work(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		return err
	}
	committed = true
	return nil
}

func discardImmediateConnection(conn immediateConnection) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
