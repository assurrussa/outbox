package pgsqlinit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	pgsql "github.com/assurrussa/outbox/infrastructure/pgsql/storage"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/pgsqlclient"
)

func Create(
	ctx context.Context,
	dsn string,
	options ...pgsqlclient.OptPoolOptionsSetter,
) (*pgsqlclient.Client, error) {
	connConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("can't parse connection string to config: %w", err)
	}

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		connConfig.ConnConfig.Host,
		connConfig.ConnConfig.User,
		connConfig.ConnConfig.Password,
		connConfig.ConnConfig.Database,
		options...,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return pool, nil
}

func CreateWithConfig(
	ctx context.Context,
	psqlConf pgsql.PSQLConfig,
	options ...pgsqlclient.OptPoolOptionsSetter,
) (*pgsqlclient.Client, error) {
	opts := []pgsqlclient.OptPoolOptionsSetter{
		pgsqlclient.WithMinConnectionsCount(psqlConf.MinConnectionsCount),
		pgsqlclient.WithMaxConnectionsCount(psqlConf.MaxConnectionsCount),
		pgsqlclient.WithMaxConnIdleTime(psqlConf.MaxConnIdleTime),
		pgsqlclient.WithMaxConnLifeTime(psqlConf.MaxConnLifeTime),
		pgsqlclient.WithSSLMode(psqlConf.SSLMode),
		pgsqlclient.WithDebug(psqlConf.DebugMode),
		pgsqlclient.WithTLSPath(psqlConf.TLSCert, psqlConf.TLSKey),
	}

	opts = append(opts, options...)

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		psqlConf.Address,
		psqlConf.Username,
		psqlConf.Password,
		psqlConf.Database,
		opts...,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return pool, nil
}
