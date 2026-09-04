package transaction

import (
	"context"
	"errors"

	picogo "github.com/picodata/picodata-go"

	coreoutbox "github.com/assurrussa/outbox/outbox"
)

// BestEffortRunner executes callbacks without atomic BEGIN/COMMIT because
// the Picodata Go client does not expose connection-pinned SQL transactions.
type BestEffortRunner struct {
	pool *picogo.Pool
}

// Manager is retained for backwards compatibility.
type Manager = BestEffortRunner

var _ coreoutbox.Transactor = (*BestEffortRunner)(nil)

func New(pool *picogo.Pool) *BestEffortRunner {
	return &BestEffortRunner{pool: pool}
}

func (m *BestEffortRunner) SupportsAtomicDLQ() bool {
	return false
}

func (m *BestEffortRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if m == nil || m.pool == nil {
		return errors.New("transaction manager is not configured")
	}

	// Picodata Go client currently doesn't expose connection-pinned SQL transactions,
	// so this backend provides best-effort callback execution without BEGIN/COMMIT.
	return fn(ctx)
}
