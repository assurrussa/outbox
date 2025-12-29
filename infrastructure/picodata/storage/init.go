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

type Option struct {
	dsn       string
	checkPing bool
	log       logger.Logger
	strategy  strats.BalanceStrategy
}

func NewOption(dsn string, strategy strats.BalanceStrategy, log logger.Logger, checkPing bool) *Option {
	if strategy == nil {
		strategy = strats.NewRoundRobinStrategy()
	}

	if log == nil {
		log = logger.Default()
	}

	return &Option{
		dsn:       dsn,
		strategy:  strategy,
		log:       log,
		checkPing: checkPing,
	}
}

var logInitOnce sync.Once

func Create(ctx context.Context, opts *Option) (*ClientPicoData, error) {
	if opts == nil {
		return nil, errors.New("nil options")
	}

	adapterLog := NewAdapterLog(opts.log)

	cfgPgx, err := pgxpool.ParseConfig(opts.dsn)
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
		picogo.WithBalanceStrategy(opts.strategy),
	)
	if err != nil {
		return nil, fmt.Errorf("picodata: unable to connect to database: %w", err)
	}

	if opts.checkPing {
		if err := pool.Ping(ctx); err != nil {
			return nil, fmt.Errorf("picodata: unable to ping database: %w", err)
		}
	}

	return newClient(pool), nil
}
