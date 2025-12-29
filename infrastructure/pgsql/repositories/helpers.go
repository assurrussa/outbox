package repositories

import (
	"context"
	"database/sql"
	"fmt"

	pgsql "github.com/assurrussa/outbox/infrastructure/pgsql/storage"
)

const QueryCountRows = `SELECT 
    table_name, (SELECT n_live_tup FROM pg_stat_user_tables WHERE relname = table_name) AS row_count
  FROM information_schema.tables WHERE table_schema = 'public';`

type Table struct {
	TableName string        `db:"table_name"`
	RowCount  sql.NullInt64 `db:"row_count"`
}

func CountRows(ctx context.Context, storage pgsql.NamedExecer) ([]Table, error) {
	const op = "CountRows"

	var data []Table
	err := storage.ScanAll(ctx, op, &data, QueryCountRows)
	if err != nil {
		return nil, fmt.Errorf("%s failed db: %w", op, err)
	}

	return data, nil
}

func CountRowsForTable(ctx context.Context, storage pgsql.NamedExecer, tableName string) (int64, error) {
	tables, err := CountRows(ctx, storage)
	if err != nil {
		return 0, err
	}

	for _, table := range tables {
		if table.TableName == tableName {
			return table.RowCount.Int64, nil
		}
	}

	return 0, nil
}
