//go:build integration

package tests

import (
	"context"
	"fmt"
	"time"

	"github.com/ilyakaznacheev/cleanenv"

	"github.com/assurrussa/outbox/shared/loadenv"
	"github.com/assurrussa/outbox/shared/tests/utilst"
	"github.com/assurrussa/outbox/shared/validator"
)

type CleanUp func(ctx context.Context)

var Config configIntegration

type configIntegration struct {
	Env      string `env:"TEST_OUTBOXLIB_ENV" env-default:"testing"`
	BasePath string `env:"TEST_OUTBOXLIB_BASE_PATH" env-default:""`
	LogLevel string `env:"TEST_OUTBOXLIB_LOG_LEVEL" env-default:"info"`

	PostgresAddress      string        `env:"TEST_OUTBOXLIB_PSQL_ADDRESS" env-default:"integration-postgres-tests" validate:"required"`
	PostgresPort         int           `env:"TEST_OUTBOXLIB_PSQL_PORT" env-default:"5432" validate:"required"`
	PostgresAddressLocal string        `env:"TEST_OUTBOXLIB_PSQL_ADDRESS_LOCAL"`
	PostgresPortLocal    int           `env:"TEST_OUTBOXLIB_PSQL_PORT_LOCAL"`
	PostgresUser         string        `env:"TEST_OUTBOXLIB_PSQL_USERNAME" env-default:"tests-service" validate:"required"`
	PostgresPassword     string        `env:"TEST_OUTBOXLIB_PSQL_PASSWORD" env-default:"tests-service" validate:"required"`
	PostgresDatabase     string        `env:"TEST_OUTBOXLIB_PSQL_DATABASENAME" env-default:"tests-db-pgsql" validate:"required"`
	PostgresSSLMode      string        `env:"TEST_OUTBOXLIB_PSQL_SSL_MODE" env-default:"disable" validate:"required"`
	PostgresDebug        bool          `env:"TEST_OUTBOXLIB_PSQL_DEBUG" env-default:"false"`
	MinConnectionsCount  int32         `env:"TEST_OUTBOXLIB_PSQL_MIN_CONN_COUNT" env-default:"1" validate:"min=1"`
	MaxConnectionsCount  int32         `env:"TEST_OUTBOXLIB_PSQL_MAX_CONN_COUNT" env-default:"100" validate:"min=1"`
	TLSCert              string        `env:"TEST_OUTBOXLIB_PSQL_TLS_CERT"`
	TLSKey               string        `env:"TEST_OUTBOXLIB_PSQL_TLS_KEY"`
	MaxConnIdleTime      time.Duration `env:"TEST_OUTBOXLIB_PSQL_MAX_CONN_IDLE_TIME" env-default:"5m" validate:"min=1s,max=1h"`
	MaxConnLifeTime      time.Duration `env:"TEST_OUTBOXLIB_PSQL_MAX_CONN_LIFE_TIME" env-default:"1h" validate:"min=1m"`
}

func init() {
	loadenv.Load()

	err := cleanenv.ReadEnv(&Config)
	if err != nil {
		panic(fmt.Errorf("read error testing env: %v", err))
	}

	err = validator.Validator.Struct(Config)
	if err != nil {
		panic(fmt.Sprintf("validate testing config: %v", err))
	}

	Config.BasePath, err = utilst.FindBasePath()
	if err != nil {
		panic(fmt.Sprintf("find base path: %v", err))
	}
}
