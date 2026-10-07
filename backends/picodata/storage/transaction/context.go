package transaction

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type key string

const (
	txKey key = "tx"
)

// WithTx selects a caller-owned query executor for repository operations. It
// does not begin, pin, commit or roll back that executor's work.
func WithTx(ctx context.Context, tx Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}

// GetTx returns the caller-supplied executor, or nil. BestEffortRunner does not
// install one when executing a callback.
func GetTx(ctx context.Context) Tx {
	tx, ok := ctx.Value(txKey).(Tx)
	if !ok {
		return nil
	}

	return tx
}

// Tx is the legacy query-executor interface. Its name makes no assertion of
// transaction ownership or atomicity; it exposes no transaction lifecycle.
type Tx interface {
	TxExecutor
}

// TxExecutor is the query surface used by Picodata repositories.
type TxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
