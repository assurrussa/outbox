package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
	// Register SQLite driver for database/sql.
	_ "modernc.org/sqlite"
)

type Option func(o *Options)

// SynchronousMode selects SQLite's commit synchronization policy.
type SynchronousMode string

const (
	SynchronousNormal SynchronousMode = "NORMAL"
	SynchronousFull   SynchronousMode = "FULL"
)

type Options struct {
	dsn             string
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connMaxIdleTime time.Duration
	checkPing       bool
	log             logger.Logger
	synchronous     SynchronousMode
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
	// Retain the registered driver's functions/collations without registering
	// any global hook. Open creates no physical connection before first use.
	connector := &sqliteConnector{driver: db.Driver(), dsn: options.dsn, synchronous: options.synchronous}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close SQLite driver handle: %w", err)
	}
	db = sql.OpenDB(connector)

	db.SetMaxOpenConns(options.maxOpenConns)
	db.SetMaxIdleConns(options.maxIdleConns)
	db.SetConnMaxLifetime(options.connMaxLifetime)
	db.SetConnMaxIdleTime(options.connMaxIdleTime)

	// Validate initialization even when the optional ping is disabled.
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := conn.Close(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("close initial SQLite connection: %w", err)
	}

	if options.checkPing {
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("ping sqlite: %w", err)
		}
	}

	return &ClientSQLite{db: db}, nil
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

// WithSynchronousMode selects NORMAL or FULL; the default is NORMAL.
// Backend settings override DSN pragmas for journal_mode, synchronous,
// busy_timeout and foreign_keys on every new physical connection, except
// legitimate memory journal modes that cannot use WAL.
func WithSynchronousMode(mode SynchronousMode) Option {
	return func(o *Options) { o.synchronous = mode }
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
	if o.synchronous == "" {
		o.synchronous = SynchronousNormal
	}
	if o.synchronous != SynchronousNormal && o.synchronous != SynchronousFull {
		return errors.New("SQLite synchronous mode must be NORMAL or FULL")
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
