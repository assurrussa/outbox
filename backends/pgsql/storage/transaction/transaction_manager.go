package transaction

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
)

type ctxKey string

const (
	KeyIsolateMode ctxKey = "trx_iso_level"
	KeyAccessMode  ctxKey = "trx_access_mode"
)

// Manager - manager transactions.
type Manager struct {
	db pgsql.Transactor
}

// New constructs TransactionManager.
func New(db pgsql.Transactor) *Manager {
	return &Manager{
		db: db,
	}
}

func (m *Manager) runTransaction(ctx context.Context, txOpts pgx.TxOptions, fn pgsql.FnCallback) (err error) {
	// If it's nested Transaction, skip initiating a new one and return FnCallback
	if tx := pgsql.GetTx(ctx); tx != nil {
		return fn(ctx)
	}

	tx, err := m.db.BeginTx(ctx, txOpts)
	if err != nil {
		return fmt.Errorf("can't begin transaction: %w", err)
	}

	ctx = pgsql.WithTx(ctx, tx)

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
		}

		if err == nil {
			err = tx.Commit(ctx)
			if err != nil {
				err = fmt.Errorf("commit failed: %w", err)
			}
		}

		if err != nil {
			if errRollback := tx.Rollback(ctx); errRollback != nil {
				err = fmt.Errorf("rollback failed: %w", errRollback)
			}
		}
	}()

	// Handle the code inside the runTransaction. If the function
	// fails, return the error and the defer function will roll back or commit otherwise.
	return fn(ctx)
}

// RunInTx adapter for Trx DB.
func (m *Manager) RunInTx(ctx context.Context, f func(ctx context.Context) error) error {
	isoLevel := pgx.ReadCommitted
	if v, ok := ctx.Value(KeyIsolateMode).(pgx.TxIsoLevel); ok {
		isoLevel = v
	}
	accessMod := pgx.ReadWrite
	if v, ok := ctx.Value(KeyAccessMode).(pgx.TxAccessMode); ok {
		accessMod = v
	}

	return m.runTransaction(ctx, pgx.TxOptions{
		IsoLevel:   isoLevel,
		AccessMode: accessMod,
	}, f)
}

// ReadCommitted execs f func in runTransaction with LevelReadCommitted isolation level.
func (m *Manager) ReadCommitted(ctx context.Context, accessMode pgx.TxAccessMode, f pgsql.FnCallback) error {
	return m.runTransaction(ctx, pgx.TxOptions{
		IsoLevel:   pgx.ReadCommitted,
		AccessMode: accessMode,
	}, f)
}

// RepeatableRead execs f func in runTransaction with LevelRepeatableRead isolation level.
func (m *Manager) RepeatableRead(ctx context.Context, accessMode pgx.TxAccessMode, f pgsql.FnCallback) error {
	return m.runTransaction(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: accessMode,
	}, f)
}

// Serializable execs f func in runTransaction with LevelSerializable isolation level.
func (m *Manager) Serializable(ctx context.Context, accessMode pgx.TxAccessMode, f pgsql.FnCallback) error {
	return m.runTransaction(ctx, pgx.TxOptions{
		IsoLevel:   pgx.Serializable,
		AccessMode: accessMode,
	}, f)
}

// WithValue adds an slog attribute to the provided context so that it will be
// included in any Record created with such context.
func WithValue(parent context.Context, key ctxKey, value any) context.Context {
	if parent == nil {
		parent = context.Background()
	}

	return context.WithValue(parent, key, value)
}
