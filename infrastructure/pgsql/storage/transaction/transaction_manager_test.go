//go:build integration

package transaction_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/infrastructure/pgsql"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/transaction"
	pgsqltests "github.com/assurrussa/outbox/infrastructure/pgsql/tests"
	"github.com/assurrussa/outbox/shared/tests"
)

type TestSuite struct {
	suite.Suite

	db       pgsql.Client
	dbHelper *pgsqltests.DBHelper
	trx      *transaction.Manager
	cleanUp  func(context.Context)
}

func NewTestSuite(t *testing.T, opts ...pgsqltests.OptionDatabase) (context.Context, context.CancelFunc, *TestSuite) {
	return tests.NewSuite[*TestSuite](t, func(t *testing.T, ctx context.Context) *TestSuite {
		db, dbHelper, cleanUp := pgsqltests.PrepareDB(ctx, t, "TestTransactorSuite", opts...)

		return &TestSuite{
			db:       db,
			dbHelper: dbHelper,
			trx:      transaction.New(db.DB()),
			cleanUp:  cleanUp,
		}
	})
}

func Test_ReadCommitted_SuccessTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.ReadCommitted(ctx, pgx.ReadWrite, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return nil
	})
	ts.Require().NoError(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().NoError(err)
	ts.Equal(nameExpected, name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().NoError(err)
	ts.Equal(actionExpected, action)
}

func Test_ReadCommitted_SuccessTransaction_ReadIsolation(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.ReadCommitted(ctx, pgx.ReadOnly, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return nil
	})
	ts.Require().Error(err)
	ts.ErrorContains(err, "25006")

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)
}

func Test_ReadCommitted_ErrorTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.ReadCommitted(ctx, pgx.ReadWrite, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return errors.New("test error transaction")
	})
	ts.Require().Error(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)
}

func Test_ReadCommitted_PanicTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.ReadCommitted(ctx, pgx.ReadWrite, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		panic(errors.New("test error transaction"))
	})
	ts.Require().Error(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)
}

func Test_RepeatableRead_SuccessTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.RepeatableRead(ctx, pgx.ReadWrite, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return nil
	})
	ts.Require().NoError(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().NoError(err)
	ts.Equal(nameExpected, name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().NoError(err)
	ts.Equal(actionExpected, action)
}

func Test_Serializable_SuccessTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	err = ts.trx.Serializable(ctx, pgx.ReadWrite, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return nil
	})
	ts.Require().NoError(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().NoError(err)
	ts.Equal(nameExpected, name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().NoError(err)
	ts.Equal(actionExpected, action)
}

func Test_RunInTx_SuccessTransaction(t *testing.T) {
	ctx, _, ts := NewTestSuite(t)
	defer ts.cleanUp(ctx)
	createTeables(ctx, ts)
	defer dropTables(ctx, ts)

	userID := 123
	nameExpected := "test-name"
	actionExpected := "insert"

	var err error
	var name string
	var action string

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().Error(err)
	ts.Equal("", name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().Error(err)
	ts.Equal("", action)

	ctx = transaction.WithValue(ctx, transaction.KeyAccessMode, pgx.ReadWrite)
	ctx = transaction.WithValue(ctx, transaction.KeyIsolateMode, pgx.Serializable)

	err = ts.trx.RunInTx(ctx, func(ctx context.Context) error {
		_, err := ts.db.DB().Exec(ctx, "testExecTrxUser", insertUser, userID, nameExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxUser: %w", err)
		}

		_, err = ts.db.DB().Exec(ctx, "testExecTrxLog", insertLogs, userID, actionExpected)
		if err != nil {
			return fmt.Errorf("testExecTrxLog: %w", err)
		}

		return nil
	})
	ts.Require().NoError(err)

	err = ts.db.DB().ScanOne(ctx, "getUser", &name, getUser, userID)
	ts.Require().NoError(err)
	ts.Equal(nameExpected, name)
	err = ts.db.DB().ScanOne(ctx, "getLog", &action, getLog, userID)
	ts.Require().NoError(err)
	ts.Equal(actionExpected, action)
}

func createTeables(ctx context.Context, ts *TestSuite) {
	ts.dbHelper.CreateTable(ctx, tableNameUsers, createTableUsers)
	ts.dbHelper.CreateTable(ctx, tableNameLogs, createTableLogs)
	ts.dbHelper.TruncateTable(ctx, tableNameUsers)
	ts.dbHelper.TruncateTable(ctx, tableNameLogs)
}

func dropTables(ctx context.Context, ts *TestSuite) {
	ts.dbHelper.DropTable(ctx, tableNameUsers)
	ts.dbHelper.DropTable(ctx, tableNameLogs)
}

const (
	tableNameUsers   = "user_any_test_name"
	tableNameLogs    = "log_any_test_name"
	createTableUsers = `create table if not exists user_any_test_name (
    id BIGSERIAL PRIMARY KEY,
    name TEXT DEFAULT NULL
);`
	createTableLogs = `create table if not exists log_any_test_name (
    user_id BIGINT PRIMARY KEY,
    action TEXT NOT NULL
);`
	insertUser = `INSERT INTO user_any_test_name (id, name) VALUES ($1, $2);`
	insertLogs = `INSERT INTO log_any_test_name (user_id, action) VALUES ($1, $2);`
	getUser    = `SELECT name FROM user_any_test_name WHERE id = $1;`
	getLog     = `SELECT action FROM log_any_test_name WHERE user_id = $1;`
)
