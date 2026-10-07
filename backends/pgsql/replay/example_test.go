package replay_test

import (
	"context"
	"errors"

	"github.com/assurrussa/outbox/outbox"

	"github.com/assurrussa/outbox/backends/pgsql"
	"github.com/assurrussa/outbox/backends/pgsql/replay"
)

func ExampleReplayer_Replay() {
	// The host supplies a migrated PostgreSQL client and its existing policy.
	stage := func(ctx context.Context, client pgsql.Client, failedID replay.JobID, requestID replay.RequestID,
		authorize func(context.Context, replay.Source) error,
		originalEffectKey func(string) (string, error),
	) (replay.Result, error) {
		r, err := replay.New(client)
		if err != nil {
			return replay.Result{}, err
		}
		return r.Replay(ctx, replay.Request{RequestID: requestID, FailedJobID: failedID},
			func(ctx context.Context, source replay.Source) (string, error) {
				if err := authorize(ctx, source); err != nil {
					return "", err
				}
				if source.Capability != (outbox.JobCapability{Name: "order.created", SchemaVersion: 2}) {
					return "", errors.New("unsupported exact handler")
				}
				// This handler already uses the returned key for its effect. Parsing
				// a key alone cannot make a non-idempotent handler safe to replay.
				return originalEffectKey(source.Payload)
			})
	}
	_ = stage
}
