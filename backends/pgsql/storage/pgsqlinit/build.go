package pgsqlinit

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
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

	address := connConfig.ConnConfig.Host
	if connConfig.ConnConfig.Port > 0 && address != "" && !strings.Contains(address, "/") {
		address = net.JoinHostPort(address, strconv.Itoa(int(connConfig.ConnConfig.Port)))
	}

	opts := make([]pgsqlclient.OptPoolOptionsSetter, 0, 1+len(options))
	opts = append(opts, pgsqlclient.WithSSLMode(parseSSLMode(dsn)))
	opts = append(opts, options...)

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		address,
		connConfig.ConnConfig.User,
		connConfig.ConnConfig.Password,
		connConfig.ConnConfig.Database,
		opts...,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return pool, nil
}

func parseSSLMode(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "disable"
	}

	if sslMode := strings.TrimSpace(u.Query().Get("sslmode")); sslMode != "" {
		return sslMode
	}

	return "disable"
}

func CreateWithConfig(
	ctx context.Context,
	psqlConf pgsql.PSQLConfig,
	options ...pgsqlclient.OptPoolOptionsSetter,
) (*pgsqlclient.Client, error) {
	opts := make([]pgsqlclient.OptPoolOptionsSetter, 0, 7+len(options))
	opts = append(opts,
		pgsqlclient.WithMinConnectionsCount(psqlConf.MinConnectionsCount),
		pgsqlclient.WithMaxConnectionsCount(psqlConf.MaxConnectionsCount),
		pgsqlclient.WithMaxConnIdleTime(psqlConf.MaxConnIdleTime),
		pgsqlclient.WithMaxConnLifeTime(psqlConf.MaxConnLifeTime),
		pgsqlclient.WithSSLMode(psqlConf.SSLMode),
		pgsqlclient.WithDebug(psqlConf.DebugMode),
		pgsqlclient.WithTLSPath(psqlConf.TLSCert, psqlConf.TLSKey),
	)

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
