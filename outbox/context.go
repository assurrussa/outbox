package outbox

import (
	"context"

	"github.com/assurrussa/outbox/shared/types"
)

type jobIDKey struct{}

func withJobID(ctx context.Context, id types.JobID) context.Context {
	return context.WithValue(ctx, jobIDKey{}, id)
}

// JobIDFromContext retrieves a job identifier previously attached to context.
func JobIDFromContext(ctx context.Context) types.JobID {
	if ctx == nil {
		return types.JobIDNil
	}

	if id, ok := ctx.Value(jobIDKey{}).(types.JobID); ok {
		return id
	}
	return types.JobIDNil
}
