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

func WithTx(ctx context.Context, tx Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}

func GetTx(ctx context.Context) Tx {
	tx, ok := ctx.Value(txKey).(Tx)
	if !ok {
		return nil
	}

	return tx
}

type Tx interface {
	// Begin(ctx context.Context) (Tx, error)
	// Commit(ctx context.Context) error
	// Rollback(ctx context.Context) error

	TxExecutor
}

type TxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
