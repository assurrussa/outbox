package pgsqlclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxUUID "github.com/vgarvardt/pgx-google-uuid/v5"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/outbox/logger"
)

// Client - postgres connection pool.
type Client struct {
	master  pgsql.DBEngine
	closers []func()
}

// NewPool - returns new Client (connection pool for postgres).
func NewPool(ctx context.Context, opts PoolOptions) (*Client, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", errors.Join(ErrOption, err))
	}

	tlsConfig, dsn, err := CreateDSN(opts)
	if err != nil {
		return nil, fmt.Errorf("creating dsn: %w", err)
	}

	connConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("can't parse connection string to config: %w", err)
	}

	connConfig.MaxConnIdleTime = opts.maxConnIdleTime
	connConfig.MaxConnLifetime = opts.maxConnLifeTime
	connConfig.MinConns = opts.minConnectionsCount
	connConfig.MaxConns = opts.maxConnectionsCount
	connConfig.ConnConfig.TLSConfig = tlsConfig
	connConfig.AfterConnect = afterConnect

	p, err := pgxpool.NewWithConfig(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("can't connect to database: %w", err)
	}

	if opts.check {
		if err := p.Ping(ctx); err != nil {
			return nil, fmt.Errorf("ping database error: %w", err)
		}
	}

	if opts.logger == nil {
		opts.logger = logger.Default().Named("postgres-master-logger")
	}

	masterDB := NewDBEngine(p, opts.environment, opts.logger)
	closers := make([]func(), 0, 1)
	closers = append(closers, masterDB.Close)

	return &Client{
		master:  masterDB,
		closers: closers,
	}, nil
}

func CreateDSN(opts PoolOptions) (*tls.Config, string, error) {
	var rawQuerySSL string
	var tlsConfig *tls.Config

	switch {
	case opts.tlsConfig != nil:
		tlsConfig = opts.tlsConfig
	case opts.tlsCert != "" && opts.tlsKey != "":
		cert, err := tls.LoadX509KeyPair(opts.tlsCert, opts.tlsKey)
		if err != nil {
			return nil, "", fmt.Errorf("failed to load x509: %w", err)
		}
		tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			ServerName:   opts.address,
			Certificates: []tls.Certificate{cert},
		}
	default:
		rawQuerySSL = "sslmode=" + opts.sslMode
	}

	dsn := (&url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(opts.username, opts.password),
		Host:     opts.address,
		Path:     opts.database,
		RawQuery: rawQuerySSL,
	}).String()

	if tlsConfig != nil {
		return tlsConfig, dsn, nil
	}

	return nil, dsn, nil
}

func (c *Client) DB() pgsql.DBEngine {
	return c.master
}

func (c *Client) Pool() *pgxpool.Pool {
	return c.master.Pool()
}

func (c *Client) Close() error {
	for _, closer := range c.closers {
		closer()
	}

	return nil
}

func afterConnect(ctx context.Context, conn *pgx.Conn) error {
	pgxUUID.Register(conn.TypeMap())

	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping database error: %w", err)
	}

	return nil
}
