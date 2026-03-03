//go:build integration

package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	strats "github.com/picodata/picodata-go/strategies"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/backends/picodata/migrator"
	picodatastorage "github.com/assurrussa/outbox/backends/picodata/storage"
	"github.com/assurrussa/outbox/outbox/logger"
	tests3 "github.com/assurrussa/outbox/shared/tests"
)

var (
	runRateLimitCh = make(chan struct{}, 10) // Максимальное кол-во паралельно запускаемых тестов
	migrationLock  sync.Mutex                // Для миграций
)

type OptionDatabase func(*OptionsDatabase)

type ReplaceTableName struct {
	Name    string
	Replace bool
}
type FnReplaceTableNameGetter func(key string) string

type OptionsDatabase struct {
	pathFilesMigration               []string
	fixedDBName                      bool
	verbose                          bool
	log                              func(string, ...any)
	databaseTableReplaces            map[string]ReplaceTableName
	databaseFnReplaceTableNameGetter FnReplaceTableNameGetter
	migrationTableName               string
}

func WithDatabasePathFilesMigration(paths ...string) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.pathFilesMigration = paths
	}
}

func WithDatabaseTableReplaces(databaseTableReplaces map[string]ReplaceTableName) OptionDatabase {
	return func(o *OptionsDatabase) {
		if o.databaseTableReplaces == nil {
			o.databaseTableReplaces = make(map[string]ReplaceTableName)
		}
		for k, v := range databaseTableReplaces {
			o.databaseTableReplaces[k] = v
		}
	}
}

func WithDatabaseFnReplaceTableNameGetter(fn FnReplaceTableNameGetter) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.databaseFnReplaceTableNameGetter = fn
	}
}

func WithDatabaseFixedName(isFixed bool) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.fixedDBName = isFixed
	}
}

func WithDatabaseVerbose(verbose bool) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.verbose = verbose
	}
}

func WithDatabaseLog(log func(string, ...any)) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.log = log
	}
}

func PrepareDB(
	ctx context.Context, t *testing.T, dbName string, opts ...OptionDatabase,
) (pool picodata.Client, db *DBHelper, cleanUp func(ctx context.Context)) {
	t.Helper()
	require.NotEmpty(t, dbName)

	opts = append(opts, TestDataReplaces()...)

	select {
	case <-ctx.Done():
		t.Logf("Warning: context done: %v", ctx.Err())
		return nil, nil, cleanUp
	case runRateLimitCh <- struct{}{}:
	}
	t.Cleanup(func() {
		<-runRateLimitCh
	})

	options := &OptionsDatabase{
		pathFilesMigration: []string{"migrations"},
		log:                t.Logf,
		fixedDBName:        false,
		verbose:            true,
	}

	for _, o := range opts {
		o(options)
	}

	if !options.fixedDBName {
		dbName += strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	options.log("database: %s", dbName)

	lg := tests3.CreateLogger(t).Named(dbName)
	options.migrationTableName = migrationTableName(dbName)

	dsn := os.Getenv("TEST_OUTBOXLIB_PICODATA_DSN")
	poolMain, err := picodatastorage.Create(
		ctx,
		dsn,
		picodatastorage.WithBalanceStrategy(strats.NewRoundRobinStrategy()),
		picodatastorage.WithLogger(lg),
		picodatastorage.WithCheckPing(true),
	)

	require.NoError(t, err)
	migrationDatabase(t, ctx, poolMain, "up", options, lg)

	db = &DBHelper{T: t, DB: poolMain, FnGetReplaceName: options.databaseFnReplaceTableNameGetter}

	return poolMain, db, func(ctx context.Context) {
		defer poolMain.Close()
		migrationDatabase(t, ctx, poolMain, "reset", options, lg)
	}
}

func migrationDatabase(
	t *testing.T,
	ctx context.Context,
	pool picodata.Client,
	command string,
	options *OptionsDatabase,
	lg logger.Logger,
) {
	t.Helper()

	dtrTables := make(map[string]string, len(options.databaseTableReplaces))
	for curTable, val := range options.databaseTableReplaces {
		if val.Replace {
			dtrTables[curTable] = val.Name
		}
	}

	var err error
	migrationLock.Lock()
	defer migrationLock.Unlock()
	// NOTE: Schema migrationDatabase is not thread-safe :(
	for _, path := range options.pathFilesMigration {
		dir := strings.Replace(path, tests3.Config.BasePath, "", 1)
		dir = filepath.Join(tests3.Config.BasePath, dir)

		err = migrator.Run(
			ctx,
			pool,
			lg,
			migrator.WithCommand(command),
			migrator.WithDirectory(dir),
			migrator.WithTableName(options.migrationTableName),
			migrator.WithSteps(1),
			migrator.WithArgs(),
			migrator.WithDatabaseTableReplacesList(dtrTables),
		)
		require.NoError(t, err)
	}
}

type DBHelper struct {
	T                *testing.T
	DB               picodata.Client
	FnGetReplaceName FnReplaceTableNameGetter
}

func (db *DBHelper) CreateTable(ctx context.Context, tableName string, sql string) {
	db.T.Helper()
	_, err := db.DB.Pool().Exec(ctx, "create_table_"+tableName, sql)
	require.NoError(db.T, err)
}

func (db *DBHelper) TruncateTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sql := fmt.Sprintf(`truncate table %s;`, tableName)
	_, err := db.DB.Pool().Exec(ctx, "truncate_table_"+tableName, sql)
	require.NoError(db.T, err)
}

func (db *DBHelper) DropTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sql := fmt.Sprintf(`drop table if exists %s;`, tableName)
	_, err := db.DB.Pool().Exec(ctx, "drop_table_"+tableName, sql)
	require.NoError(db.T, err)
}

func migrationTableName(dbName string) string {
	const prefix = "picodata_db_version_"
	if dbName == "" {
		return prefix + "test"
	}

	var b strings.Builder
	for i, r := range dbName {
		switch {
		case r == '_':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "test"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "t_" + name
	}

	return prefix + name
}
