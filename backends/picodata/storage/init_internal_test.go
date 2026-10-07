package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckPoolOwnership(t *testing.T) {
	pingErr := errors.New("owned ping failure")
	for _, test := range []struct {
		name    string
		err     error
		enabled bool
		pings   int
		closes  int
	}{
		{name: "ping failure closes owned pool", err: pingErr, enabled: true, pings: 1, closes: 1},
		{name: "cancellation closes owned pool", err: context.Canceled, enabled: true, pings: 1, closes: 1},
		{name: "success transfers ownership", enabled: true, pings: 1},
		{name: "disabled ping transfers ownership", err: pingErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			pool := &initializationTestPool{pingErr: test.err, onPing: func(actual context.Context) {
				require.Equal(t, ctx, actual)
			}}
			err := checkPool(ctx, pool, test.enabled)
			if test.enabled && test.err != nil {
				require.ErrorIs(t, err, test.err)
				require.ErrorContains(t, err, "picodata: unable to ping database")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.pings, pool.pings)
			require.Equal(t, test.closes, pool.closes)
		})
	}
}

type initializationTestPool struct {
	onPing  func(context.Context)
	pingErr error
	pings   int
	closes  int
}

func (p *initializationTestPool) Ping(ctx context.Context) error {
	p.onPing(ctx)
	p.pings++
	return p.pingErr
}

func (p *initializationTestPool) Close() { p.closes++ }
