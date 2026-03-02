package transaction

import (
	"context"
	"database/sql"
)

type key string

const txKey key = "mysql_tx"

func WithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}

func GetTx(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(txKey).(*sql.Tx)
	return tx
}

type TxExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
