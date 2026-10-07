package picodata

import (
	picogo "github.com/picodata/picodata-go"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
)

type Client interface {
	Pool() *picogo.Pool
	Close() error
}

// ClientTransaction is the legacy client shape exposing a best-effort callback
// runner. TxPool does not provide connection-pinned BEGIN/COMMIT/ROLLBACK or an
// atomic business-write/enqueue boundary. The runner reports SupportsAtomicDLQ
// as false; non-atomic DLQ requires explicit host opt-in.
type ClientTransaction interface {
	Client
	TxPool() *transaction.Manager
}
