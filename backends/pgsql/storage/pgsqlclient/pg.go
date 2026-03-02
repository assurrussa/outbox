package pgsqlclient

import (
	"context"
	"fmt"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	pgsql2 "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/prettier"
	"github.com/assurrussa/outbox/outbox/logger"
)

type clientPG struct {
	pool *pgxpool.Pool
	env  string
	log  logger.Logger
}

func NewDBEngine(pool *pgxpool.Pool, env string, log logger.Logger) pgsql2.DBEngine {
	return &clientPG{
		pool: pool,
		env:  env,
		log:  log,
	}
}

func (c *clientPG) ScanOne(ctx context.Context, operationName string, dest any, sql string, args ...any) error {
	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "ScanOne."+operationName, sql, args)
	defer closer()

	rows, err := c.Query(ctx, operationName, sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	return pgxscan.ScanOne(dest, rows)
}

func (c *clientPG) ScanAll(ctx context.Context, operationName string, dest any, sql string, args ...any) error {
	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "ScanAll."+operationName, sql, args)
	defer closer()

	rows, err := c.Query(ctx, operationName, sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	return pgxscan.ScanAll(dest, rows)
}

func (c *clientPG) ScanOnex(ctx context.Context, operationName string, dest any, sqlizer pgsql2.Sqlizer) error {
	sql, args, err := sqlizer.ToSql()
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "ScanOnex."+operationName, sql, args)
	defer closer()

	rows, err := c.Query(ctx, operationName, sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	return pgxscan.ScanOne(dest, rows)
}

func (c *clientPG) ScanAllx(ctx context.Context, operationName string, dest any, sqlizer pgsql2.Sqlizer) error {
	sql, args, err := sqlizer.ToSql()
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "ScanAllx."+operationName, sql, args)
	defer closer()

	rows, err := c.Query(ctx, operationName, sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	return pgxscan.ScanAll(dest, rows)
}

// Query - pgx.Query.
func (c *clientPG) Query(ctx context.Context, operationName string, sql string, args ...any) (pgx.Rows, error) {
	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "Query."+operationName, sql, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.Query(ctx, sql, args...) //nolint:sqlclosecheck // because not here closed conn
	}

	return c.pool.Query(ctx, sql, args...) //nolint:sqlclosecheck // because not here closed conn
}

// Exec - pgx.Exec.
func (c *clientPG) Exec(ctx context.Context, operationName string, sql string, args ...any) (pgconn.CommandTag, error) {
	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "Exec."+operationName, sql, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.Exec(ctx, sql, args...)
	}

	return c.pool.Exec(ctx, sql, args...)
}

// QueryRow - pgx.QueryRow.
func (c *clientPG) QueryRow(ctx context.Context, operationName string, sql string, args ...any) pgx.Row {
	c.logQuery(ctx, sql, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "QueryRow."+operationName, sql, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.QueryRow(ctx, sql, args...)
	}

	return c.pool.QueryRow(ctx, sql, args...)
}

// Getx - aka QueryRow.
func (c *clientPG) Getx(ctx context.Context, operationName string, dest any, sqlizer pgsql2.Sqlizer) error {
	query, args, err := sqlizer.ToSql()
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	c.logQuery(ctx, query, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "Getx."+operationName, query, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return pgxscan.Get(ctx, tx, dest, query, args...)
	}

	return pgxscan.Get(ctx, c.pool, dest, query, args...)
}

// Selectx - aka Query.
func (c *clientPG) Selectx(ctx context.Context, operationName string, dest any, sqlizer pgsql2.Sqlizer) error {
	query, args, err := sqlizer.ToSql()
	if err != nil {
		return fmt.Errorf("postgres: to sql: %w", err)
	}

	c.logQuery(ctx, query, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "Selectx."+operationName, query, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return pgxscan.Get(ctx, tx, dest, query, args...)
	}

	return pgxscan.Select(ctx, c.pool, dest, query, args...)
}

// Execx - aka Exec.
func (c *clientPG) Execx(ctx context.Context, operationName string, sqlizer pgsql2.Sqlizer) (pgconn.CommandTag, error) {
	query, args, err := sqlizer.ToSql()
	if err != nil {
		return pgconn.CommandTag{}, fmt.Errorf("postgres: to sql: %w", err)
	}

	c.logQuery(ctx, query, operationName, args)
	ctx, closer := pgsql2.CreateSpan(ctx, "Execx."+operationName, query, args)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.Exec(ctx, query, args...)
	}

	return c.pool.Exec(ctx, query, args...)
}

// BeginTx - pgx.BeginTx.
func (c *clientPG) BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error) {
	return c.pool.BeginTx(ctx, txOptions)
}

// SendBatch - pgx.SendBatch.
func (c *clientPG) SendBatch(ctx context.Context, operationName string, b *pgx.Batch) pgx.BatchResults {
	ctx, closer := pgsql2.CreateSpan(ctx, "SendBatch."+operationName, "", nil)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.SendBatch(ctx, b)
	}

	return c.pool.SendBatch(ctx, b)
}

// CopyFrom - pgx.CopyFrom.
func (c *clientPG) CopyFrom(
	ctx context.Context, operationName string, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource,
) (int64, error) {
	ctx, closer := pgsql2.CreateSpan(ctx, "CopyFrom."+operationName, "", nil)
	defer closer()

	if tx := pgsql2.GetTx(ctx); tx != nil {
		return tx.CopyFrom(ctx, tableName, columnNames, rowSrc)
	}

	return c.pool.CopyFrom(ctx, tableName, columnNames, rowSrc)
}

func (c *clientPG) Ping(ctx context.Context) error {
	return c.pool.Ping(ctx)
}

func (c *clientPG) Pool() *pgxpool.Pool {
	return c.pool
}

// Close - close pool.
func (c *clientPG) Close() {
	c.log.InfoContext(context.Background(), "pgsql client: closing connection")
	c.pool.Close()
}

func (c *clientPG) logQuery(ctx context.Context, sql string, operationName string, args []any) {
	prettier.LogQuery(ctx, c.log, c.env, sql, operationName, args)
}
