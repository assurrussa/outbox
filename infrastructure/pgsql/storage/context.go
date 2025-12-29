package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type key string

const (
	txKey key = "tx"
)

func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}

func GetTx(ctx context.Context) pgx.Tx {
	tx, ok := ctx.Value(txKey).(pgx.Tx)
	if !ok {
		return nil
	}

	return tx
}
