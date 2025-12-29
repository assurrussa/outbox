package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	picogo "github.com/picodata/picodata-go"
	picolog "github.com/picodata/picodata-go/logger"
	strats "github.com/picodata/picodata-go/strategies"

	"github.com/assurrussa/outbox/outbox/logger"
)

type Option func(o *Options)

type Options struct {
	dsn       string
	checkPing bool
	log       logger.Logger
	strategy  strats.BalanceStrategy
}

var logInitOnce sync.Once

func Create(ctx context.Context, dsn string, opts ...Option) (*ClientPicoData, error) {
	options := &Options{
		dsn:       dsn,
		strategy:  strats.NewRoundRobinStrategy(),
		log:       logger.Default(),
		checkPing: true,
	}

	for _, opt := range opts {
		opt(options)
	}

	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	adapterLog := NewAdapterLog(options.log)

	cfgPgx, err := pgxpool.ParseConfig(options.dsn)
	if err != nil {
		return nil, err
	}

	var logErr error
	logInitOnce.Do(func() {
		picolog.SetDefaultLogger(adapterLog)
		if err := picolog.SetLevel(adapterLog.level); err != nil {
			logErr = fmt.Errorf("picodata: set log level: %w", err)
		}
	})
	if logErr != nil {
		return nil, logErr
	}

	pool, err := picogo.NewWithConfig(
		ctx,
		cfgPgx,
		picogo.WithBalanceStrategy(options.strategy),
	)
	if err != nil {
		return nil, fmt.Errorf("picodata: unable to connect to database: %w", err)
	}

	if options.checkPing {
		if err := pool.Ping(ctx); err != nil {
			return nil, fmt.Errorf("picodata: unable to ping database: %w", err)
		}
	}

	return newClient(pool), nil
}

func WithDSN(dsn string) Option {
	return func(o *Options) {
		o.dsn = dsn
	}
}

func WithCheckPing(checkPing bool) Option {
	return func(o *Options) {
		o.checkPing = checkPing
	}
}

func WithLogger(log logger.Logger) Option {
	return func(o *Options) {
		o.log = log
	}
}

func WithBalanceStrategy(strategy strats.BalanceStrategy) Option {
	return func(o *Options) {
		o.strategy = strategy
	}
}

func (o *Options) Validate() error {
	if o == nil {
		return errors.New("nil options")
	}
	if o.log == nil {
		return errors.New("nil logger")
	}
	if o.strategy == nil {
		return errors.New("nil balance strategy")
	}
	if o.dsn == "" {
		return errors.New("nil dsn")
	}

	return nil
}
