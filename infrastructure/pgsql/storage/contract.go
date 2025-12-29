package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:generate toolsmocks

type FnCallback func(ctx context.Context) error

// Client клиент для работы с БД.
type Client interface {
	DB() DBEngine
	Close() error
}

// Sqlizer - something that can build sql query.
type Sqlizer interface {
	ToSql() (sql string, args []any, err error)
}

// Transactor интерфейс для работы с транзакциями.
type Transactor interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// TxManager менеджер транзакций, который выполняет указанный пользователем обработчик в транзакции.
type TxManager interface {
	RunInTx(ctx context.Context, f func(context.Context) error) error
	ReadCommitted(ctx context.Context, accessMode pgx.TxAccessMode, fn FnCallback) error
	RepeatableRead(ctx context.Context, accessMode pgx.TxAccessMode, fn FnCallback) error
	Serializable(ctx context.Context, accessMode pgx.TxAccessMode, fn FnCallback) error
}

// Pinger интерфейс для проверки соединения с БД.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NamedExecer - pgx scan api.
type NamedExecer interface {
	ScanOne(ctx context.Context, operationName string, dest any, sql string, args ...any) error
	ScanAll(ctx context.Context, operationName string, dest any, sql string, args ...any) error
}

// NamedExecerSqlizer - pgx scan extended api.
type NamedExecerSqlizer interface {
	ScanOnex(ctx context.Context, operationName string, dest any, sqlizer Sqlizer) error
	ScanAllx(ctx context.Context, operationName string, dest any, sqlizer Sqlizer) error
}

// QueryExecer - pgx common api.
type QueryExecer interface {
	QueryRow(ctx context.Context, operationName string, sql string, args ...any) pgx.Row
	Query(ctx context.Context, operationName string, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, operationName string, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// QueryExecerSqlizer улучшенный QueryExecer.
type QueryExecerSqlizer interface {
	// Getx - aka QueryRow
	Getx(ctx context.Context, operationName string, dest any, sqlizer Sqlizer) error
	// Selectx - aka Query
	Selectx(ctx context.Context, operationName string, dest any, sqlizer Sqlizer) error
	// Execx - aka Exec
	Execx(ctx context.Context, operationName string, sqlizer Sqlizer) (pgconn.CommandTag, error)
}

// BatcherExtended - для массовой вставки и копирования.
type BatcherExtended interface {
	SendBatch(ctx context.Context, operationName string, b *pgx.Batch) pgx.BatchResults
	CopyFrom(
		ctx context.Context,
		operationName string,
		tableName pgx.Identifier,
		columnNames []string,
		rowSrc pgx.CopyFromSource,
	) (int64, error)
}

// SQLExecer комбинирует NamedExecer и QueryExecer.
type SQLExecer interface {
	NamedExecer
	NamedExecerSqlizer
	QueryExecer
	QueryExecerSqlizer
}

// DBEngine is a common database query interface.
type DBEngine interface {
	SQLExecer
	BatcherExtended
	Transactor
	Pinger
	DBPgxEnginePool
	Close()
}

type DBPgxEnginePool interface {
	Pool() *pgxpool.Pool
}
