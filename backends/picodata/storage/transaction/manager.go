package transaction

import (
	"context"
	"errors"

	picogo "github.com/picodata/picodata-go"
)

type Manager struct {
	pool *picogo.Pool
}

func New(pool *picogo.Pool) *Manager {
	return &Manager{pool: pool}
}

func (m *Manager) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if m == nil || m.pool == nil {
		return errors.New("transaction manager is not configured")
	}

	// Picodata Go client currently doesn't expose connection-pinned SQL transactions,
	// so this backend provides best-effort callback execution without BEGIN/COMMIT.
	return fn(ctx)
}
