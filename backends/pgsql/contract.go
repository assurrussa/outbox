package pgsql

import (
	"github.com/assurrussa/outbox/backends/pgsql/storage"
)

type Client interface {
	DB() storage.DBEngine
	Close() error
}
