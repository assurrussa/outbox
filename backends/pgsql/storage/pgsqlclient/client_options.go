package pgsqlclient

import (
	"crypto/tls"
	"errors"
	"time"

	"github.com/assurrussa/outbox/outbox/logger"
)

var ErrOption = errors.New("pgsql client invalid option")

type OptPoolOptionsSetter func(o *PoolOptions)

type PoolOptions struct {
	address             string
	username            string
	password            string
	database            string
	sslMode             string
	debug               bool
	maxConnIdleTime     time.Duration
	maxConnLifeTime     time.Duration
	minConnectionsCount int32
	maxConnectionsCount int32
	environment         string
	tlsCert             string
	tlsKey              string
	tlsConfig           *tls.Config
	check               bool
	logger              logger.Logger
}

func NewOptions(
	address string,
	username string,
	password string,
	database string,
	options ...OptPoolOptionsSetter,
) PoolOptions {
	o := PoolOptions{
		address:             address,
		username:            username,
		password:            password,
		database:            database,
		logger:              logger.Default(),
		check:               true,
		environment:         "prod",
		sslMode:             "disable",
		debug:               false,
		tlsConfig:           nil,
		tlsCert:             "",
		tlsKey:              "",
		maxConnectionsCount: 10,
		minConnectionsCount: 5,
		maxConnIdleTime:     5 * time.Minute,
		maxConnLifeTime:     1 * time.Hour,
	}

	for _, opt := range options {
		opt(&o)
	}

	return o
}

func (o *PoolOptions) Validate() error {
	if o == nil {
		return errors.New("nil options")
	}
	if o.logger == nil {
		return errors.New("nil logger")
	}
	if o.maxConnectionsCount < 1 {
		return errors.New("max connections count must be greater than zero")
	}
	if o.minConnectionsCount < 1 {
		return errors.New("min connections count must be greater than zero")
	}

	return nil
}

func WithTLSConfig(cfg *tls.Config) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.tlsConfig = cfg
	}
}

func WithTLSPath(cert string, key string) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.tlsCert = cert
		o.tlsKey = key
	}
}

func WithLogger(logger logger.Logger) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.logger = logger
	}
}

func WithDebug(debug bool) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.debug = debug
	}
}

func WithCheck(check bool) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.check = check
	}
}

func WithEnvironment(env string) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.environment = env
	}
}

func WithMaxConnIdleTime(maxConnIdleTime time.Duration) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.maxConnIdleTime = maxConnIdleTime
	}
}

func WithMaxConnLifeTime(maxConnLifeTime time.Duration) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.maxConnLifeTime = maxConnLifeTime
	}
}

func WithMaxConnectionsCount(maxConnections int32) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.maxConnectionsCount = maxConnections
	}
}

func WithMinConnectionsCount(minConnections int32) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.minConnectionsCount = minConnections
	}
}

func WithSSLMode(sslMode string) OptPoolOptionsSetter {
	return func(o *PoolOptions) {
		o.sslMode = sslMode
	}
}
