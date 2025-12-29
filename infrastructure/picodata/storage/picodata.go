package storage

import (
	picogo "github.com/picodata/picodata-go"

	"github.com/assurrussa/outbox/infrastructure/picodata/storage/transaction"
)

type ClientPicoData struct {
	pool *picogo.Pool
}

func newClient(pool *picogo.Pool) *ClientPicoData {
	return &ClientPicoData{pool: pool}
}

func (p *ClientPicoData) Close() error {
	if p.pool != nil {
		p.pool.Close()
	}

	return nil
}

func (p *ClientPicoData) Pool() *picogo.Pool {
	return p.pool
}

func (p *ClientPicoData) TxPool() *transaction.Manager {
	return transaction.New(p.Pool())
}
