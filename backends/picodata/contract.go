package picodata

import (
	picogo "github.com/picodata/picodata-go"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
)

type Client interface {
	Pool() *picogo.Pool
	Close() error
}

type ClientTransaction interface {
	Client
	TxPool() *transaction.Manager
}
