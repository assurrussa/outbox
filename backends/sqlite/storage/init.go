package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// Register SQLite driver for database/sql.
	_ "modernc.org/sqlite"

	"github.com/assurrussa/outbox/outbox/logger"
)

type Option func(o *Options)

type Options struct {
	dsn             string
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connMaxIdleTime time.Duration
	checkPing       bool
	log             logger.Logger
}

type ClientSQLite struct {
	db *sql.DB
}

func Create(ctx context.Context, dsn string, opts ...Option) (*ClientSQLite, error) {
	options := &Options{
		dsn:             dsn,
		maxOpenConns:    10,
		maxIdleConns:    10,
		connMaxLifetime: 0,
		connMaxIdleTime: 0,
		checkPing:       true,
		log:             logger.Default(),
	}

	for _, opt := range opts {
		opt(options)
	}

	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	db, err := sql.Open("sqlite", options.dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(options.maxOpenConns)
	db.SetMaxIdleConns(options.maxIdleConns)
	db.SetConnMaxLifetime(options.connMaxLifetime)
	db.SetConnMaxIdleTime(options.connMaxIdleTime)

	if err := configureSQLite(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if options.checkPing {
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("ping sqlite: %w", err)
		}
	}

	return &ClientSQLite{db: db}, nil
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	queries := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA synchronous=NORMAL;",
	}

	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite pragma %q: %w", q, err)
		}
	}

	return nil
}

func WithDSN(dsn string) Option {
	return func(o *Options) {
		o.dsn = dsn
	}
}

func WithCheckPing(check bool) Option {
	return func(o *Options) {
		o.checkPing = check
	}
}

func WithLogger(log logger.Logger) Option {
	return func(o *Options) {
		o.log = log
	}
}

func WithMaxOpenConns(v int) Option {
	return func(o *Options) {
		o.maxOpenConns = v
	}
}

func WithMaxIdleConns(v int) Option {
	return func(o *Options) {
		o.maxIdleConns = v
	}
}

func WithConnMaxLifetime(v time.Duration) Option {
	return func(o *Options) {
		o.connMaxLifetime = v
	}
}

func WithConnMaxIdleTime(v time.Duration) Option {
	return func(o *Options) {
		o.connMaxIdleTime = v
	}
}

func (o *Options) Validate() error {
	if o == nil {
		return errors.New("nil options")
	}
	if o.dsn == "" {
		return errors.New("nil dsn")
	}
	if o.log == nil {
		return errors.New("nil logger")
	}
	if o.maxOpenConns < 1 {
		return errors.New("max open conns must be >= 1")
	}
	if o.maxIdleConns < 0 {
		return errors.New("max idle conns must be >= 0")
	}

	return nil
}

func (c *ClientSQLite) DB() *sql.DB {
	return c.db
}

func (c *ClientSQLite) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}
