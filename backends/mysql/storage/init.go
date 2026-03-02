package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// Register MySQL driver for database/sql.
	_ "github.com/go-sql-driver/mysql"

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

type ClientMySQL struct {
	db *sql.DB
}

func Create(ctx context.Context, dsn string, opts ...Option) (*ClientMySQL, error) {
	options := &Options{
		dsn:             dsn,
		maxOpenConns:    10,
		maxIdleConns:    5,
		connMaxLifetime: time.Hour,
		connMaxIdleTime: 5 * time.Minute,
		checkPing:       true,
		log:             logger.Default(),
	}

	for _, opt := range opts {
		opt(options)
	}

	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	db, err := sql.Open("mysql", options.dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(options.maxOpenConns)
	db.SetMaxIdleConns(options.maxIdleConns)
	db.SetConnMaxLifetime(options.connMaxLifetime)
	db.SetConnMaxIdleTime(options.connMaxIdleTime)

	if options.checkPing {
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("ping mysql: %w", err)
		}
	}

	return &ClientMySQL{db: db}, nil
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

func (c *ClientMySQL) DB() *sql.DB {
	return c.db
}

func (c *ClientMySQL) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}
