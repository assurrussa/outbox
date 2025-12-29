package pgsqlinit

import (
	"context"
	"fmt"

	pgsql "github.com/assurrussa/outbox/infrastructure/pgsql/storage"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/pgsqlclient"
)

func NewPool(
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
