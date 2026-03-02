package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/backends/sqlite/migrator"
	sqlitestorage "github.com/assurrussa/outbox/backends/sqlite/storage"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tests/utilst"
)

type OptionDatabase func(*OptionsDatabase)

type OptionsDatabase struct {
	pathFilesMigration []string
	log                func(string, ...any)
}

var migrationLock sync.Mutex

func WithDatabasePathFilesMigration(paths ...string) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.pathFilesMigration = paths
	}
}

func WithDatabaseLog(log func(string, ...any)) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.log = log
	}
}

func PrepareDB(
	ctx context.Context,
	t *testing.T,
	dbName string,
	opts ...OptionDatabase,
) (sqlite.Client, *DBHelper, func(context.Context)) {
	t.Helper()

	options := &OptionsDatabase{
		pathFilesMigration: []string{"migrations"},
		log:                t.Logf,
	}
	for _, o := range opts {
		o(options)
	}

	if dbName == "" {
		dbName = "sqlite-test"
	}
	dbName += "-" + strings.ReplaceAll(uuid.New().String(), "-", "")

	dir := t.TempDir()
	dsn := filepath.Join(dir, dbName+".db")

	pool, err := sqlitestorage.Create(ctx, dsn, sqlitestorage.WithCheckPing(true))
	require.NoError(t, err)

	basePath, err := utilst.FindBasePath()
	require.NoError(t, err)

	lg := logger.Default().Named(dbName)
	migrationLock.Lock()
	defer migrationLock.Unlock()

	for _, path := range options.pathFilesMigration {
		migrationDir := strings.Replace(path, basePath, "", 1)
		migrationDir = filepath.Join(basePath, migrationDir)
		err = migrator.Run(
			ctx,
			pool.DB(),
			lg,
			migrator.WithCommand("up"),
			migrator.WithDirectory(migrationDir),
			migrator.WithArgs(),
		)
		require.NoError(t, err)
	}

	helper := &DBHelper{T: t, DB: pool}

	return pool, helper, func(context.Context) {
		require.NoError(t, pool.Close())
	}
}

type DBHelper struct {
	T  *testing.T
	DB sqlite.Client
}

func (db *DBHelper) CreateTable(ctx context.Context, tableName string, sqlStmt string) {
	db.T.Helper()
	_, err := db.DB.DB().ExecContext(ctx, sqlStmt)
	require.NoError(db.T, err, tableName)
}

func (db *DBHelper) TruncateTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sqlStmt := fmt.Sprintf(`DELETE FROM %s;`, tableName)
	_, err := db.DB.DB().ExecContext(ctx, sqlStmt)
	require.NoError(db.T, err)
}

func (db *DBHelper) DropTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sqlStmt := fmt.Sprintf(`DROP TABLE IF EXISTS %s;`, tableName)
	_, err := db.DB.DB().ExecContext(ctx, sqlStmt)
	require.NoError(db.T, err)
}
