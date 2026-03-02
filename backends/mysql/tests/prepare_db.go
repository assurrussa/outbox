//go:build integration

package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/backends/mysql/migrator"
	mysqlstorage "github.com/assurrussa/outbox/backends/mysql/storage"
	"github.com/assurrussa/outbox/outbox/logger"
	sharedtests "github.com/assurrussa/outbox/shared/tests"
)

var (
	runRateLimitCh = make(chan struct{}, 3)
	migrationLock  sync.Mutex
)

type OptionDatabase func(*OptionsDatabase)

type OptionsDatabase struct {
	pathFilesMigration []string
	fixedDBName        bool
	verbose            bool
	log                func(string, ...any)
}

func WithDatabasePathFilesMigration(paths ...string) OptionDatabase {
	return func(o *OptionsDatabase) {
		o.pathFilesMigration = paths
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
	ctx context.Context,
	t *testing.T,
	dbName string,
	opts ...OptionDatabase,
) (mysql.Client, *DBHelper, func(context.Context)) {
	t.Helper()
	require.NotEmpty(t, dbName)

	select {
	case <-ctx.Done():
		t.Logf("Warning: context done: %v", ctx.Err())
		return nil, nil, func(context.Context) {}
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

	lg := sharedtests.CreateLogger(t).Named(dbName)

	mainDSN := buildDSN("")
	poolMain, err := mysqlstorage.Create(
		ctx,
		mainDSN,
		mysqlstorage.WithLogger(lg),
		mysqlstorage.WithCheckPing(true),
	)
	require.NoError(t, err)

	require.NoError(t, createDatabase(ctx, dbName, poolMain))

	dbDSN := buildDSN(dbName)
	pool, err := mysqlstorage.Create(
		ctx,
		dbDSN,
		mysqlstorage.WithLogger(lg),
		mysqlstorage.WithCheckPing(true),
	)
	require.NoError(t, err)

	migrateDatabase(t, ctx, pool, "up", options, lg)

	helper := &DBHelper{T: t, DB: pool}

	return pool, helper, func(ctx context.Context) {
		assert.NoError(t, pool.Close())
		assert.NoError(t, dropDatabaseIfExists(ctx, dbName, poolMain))
		assert.NoError(t, poolMain.Close())
	}
}

func migrateDatabase(
	t *testing.T,
	ctx context.Context,
	pool mysql.Client,
	command string,
	options *OptionsDatabase,
	lg logger.Logger,
) {
	t.Helper()

	migrationLock.Lock()
	defer migrationLock.Unlock()

	for _, path := range options.pathFilesMigration {
		dir := strings.Replace(path, sharedtests.Config.BasePath, "", 1)
		dir = filepath.Join(sharedtests.Config.BasePath, dir)
		err := migrator.Run(
			ctx,
			pool.DB(),
			lg,
			migrator.WithCommand(command),
			migrator.WithDirectory(dir),
			migrator.WithArgs(),
		)
		require.NoError(t, err)
	}
}

func createDatabase(ctx context.Context, dbName string, pool mysql.Client) error {
	if err := dropDatabaseIfExists(ctx, dbName, pool); err != nil {
		return fmt.Errorf("drop db %s: %w", dbName, err)
	}

	query := fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName)
	if _, err := pool.DB().ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create db %s: %w", dbName, err)
	}

	return nil
}

func dropDatabaseIfExists(ctx context.Context, dbName string, pool mysql.Client) error {
	query := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)
	_, err := pool.DB().ExecContext(ctx, query)
	return err
}

func buildDSN(database string) string {
	address := sharedtests.Config.MySQLAddress
	useLocalAddress := sharedtests.Config.MySQLAddressLocal != ""
	if useLocalAddress {
		address = sharedtests.Config.MySQLAddressLocal
	}
	port := sharedtests.Config.MySQLPort
	if sharedtests.Config.MySQLPortLocal > 0 {
		port = sharedtests.Config.MySQLPortLocal
	}
	if useLocalAddress && strings.EqualFold(address, "localhost") {
		// Docker publishes integration MySQL to 127.0.0.1 in compose; avoid ::1 resolution issues.
		address = "127.0.0.1"
	}

	targetDB := database
	if targetDB == "" {
		targetDB = "mysql"
	}

	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=UTC&multiStatements=true",
		sharedtests.Config.MySQLUser,
		sharedtests.Config.MySQLPassword,
		address,
		port,
		targetDB,
	)
}

type DBHelper struct {
	T  *testing.T
	DB mysql.Client
}

func (db *DBHelper) CreateTable(ctx context.Context, tableName string, sql string) {
	db.T.Helper()
	_, err := db.DB.DB().ExecContext(ctx, sql)
	require.NoError(db.T, err, tableName)
}

func (db *DBHelper) TruncateTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sql := fmt.Sprintf(`TRUNCATE TABLE %s;`, tableName)
	_, err := db.DB.DB().ExecContext(ctx, sql)
	require.NoError(db.T, err)
}

func (db *DBHelper) DropTable(ctx context.Context, tableName string) {
	db.T.Helper()
	sql := fmt.Sprintf(`DROP TABLE IF EXISTS %s;`, tableName)
	_, err := db.DB.DB().ExecContext(ctx, sql)
	require.NoError(db.T, err)
}
