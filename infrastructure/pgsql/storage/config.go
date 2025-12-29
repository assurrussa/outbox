//nolint:lll // it's configuration
package storage

import "time"

type PSQLConfig struct {
	Address             string        `toml:"address" long:"psql-address" env:"PSQL_ADDRESS" value-default:"127.0.0.1:5432" validate:"required,hostname_port"`
	Username            string        `toml:"username" long:"psql-username" env:"PSQL_USERNAME" value-default:"admin" validate:"required"`
	Password            string        `toml:"password" long:"psql-password" env:"PSQL_PASSWORD" value-default:"admin" validate:"required"`
	Database            string        `toml:"database" long:"psql-database" env:"PSQL_DATABASE" value-default:"dbname" validate:"required"`
	SSLMode             string        `toml:"ssl_mode" long:"psql-ssl-mode" env:"PSQL_SSL_MODE" value-default:"disable" validate:"required"`
	DebugMode           bool          `toml:"debug_mode" long:"psql-debug" env:"PSQL_DEBUG" value-default:"false" validate:"boolean"`
	MinConnectionsCount int32         `toml:"min_conn_count" long:"psql-min-conn-count" env:"PSQL_MIN_CONN_COUNT" value-default:"5" validate:"min=1"`
	MaxConnectionsCount int32         `toml:"max_conn_count" long:"psql-max-conn-count" env:"PSQL_MAX_CONN_COUNT" value-default:"10" validate:"min=1"`
	TLSCert             string        `toml:"tls_cert" long:"psql-tls-cert" env:"PSQL_TLS_CERT"`
	TLSKey              string        `toml:"tls_key" long:"psql-tls-key" env:"PSQL_TLS_KEY"`
	MaxConnIdleTime     time.Duration `toml:"max_conn_idle_time" long:"psql-max-conn-idle" env:"PSQL_MAX_CONN_IDLE_TIME" value-default:"5m" validate:"min=1s,max=1h"`
	MaxConnLifeTime     time.Duration `toml:"max_conn_life_time" long:"psql-max-conn-life" env:"PSQL_MAX_CONN_LIFE_TIME" value-default:"1h" validate:"min=1m"`
}
