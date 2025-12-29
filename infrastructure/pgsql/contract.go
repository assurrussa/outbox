package pgsql

import (
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage"
)

type Client interface {
	DB() storage.DBEngine
	Close() error
}
