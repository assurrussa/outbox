package outbox

import (
	"context"
	"sync"
)

// leaseMutex serializes the ledger while allowing a finalizer to stop waiting
// for an in-flight heartbeat when its own budget expires. Its zero value is ready
// to use, just like the mutex used for the ledger's non-cancellable operations.
type leaseMutex struct {
	once sync.Once
	held chan struct{}
}

func (m *leaseMutex) init() {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
}

func (m *leaseMutex) Lock() {
	m.init()
	m.held <- struct{}{}
}

func (m *leaseMutex) LockContext(ctx context.Context) error {
	m.init()
	select {
	case m.held <- struct{}{}:
		if cause := context.Cause(ctx); cause != nil {
			m.Unlock()
			return cause
		}
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (m *leaseMutex) Unlock() {
	<-m.held
}
