package transaction

import (
	"context"
	"errors"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	picogo "github.com/picodata/picodata-go"
)

// BestEffortRunner executes callbacks without atomic BEGIN/COMMIT because
// the Picodata Go client does not expose connection-pinned SQL transactions.
type BestEffortRunner struct {
	pool *picogo.Pool
}

// Manager is retained for backwards compatibility. Its callbacks have the same
// non-atomic behavior as BestEffortRunner despite the legacy transaction name.
type Manager = BestEffortRunner

var _ coreoutbox.Transactor = (*BestEffortRunner)(nil)

// New creates a best-effort runner; it does not acquire or pin a connection.
func New(pool *picogo.Pool) *BestEffortRunner {
	return &BestEffortRunner{pool: pool}
}

// SupportsAtomicDLQ is always false because RunInTx provides no atomic boundary.
func (m *BestEffortRunner) SupportsAtomicDLQ() bool {
	return false
}

// RunInTx invokes fn once with the supplied context and returns its error.
// It does not create a transaction or install a Tx in the context. A callback
// error cannot roll back earlier writes or make enqueue atomic with them.
func (m *BestEffortRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if m == nil || m.pool == nil {
		return errors.New("transaction manager is not configured")
	}

	// Picodata Go client currently doesn't expose connection-pinned SQL transactions,
	// so this backend provides best-effort callback execution without BEGIN/COMMIT.
	return fn(ctx)
}
