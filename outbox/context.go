package outbox

import (
	"context"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

type jobMetadataKey struct{}

// JobMetadata identifies the currently executing persisted attempt.
type JobMetadata struct {
	ID      types.JobID
	Attempt int
}

func withJobMetadata(ctx context.Context, job models.Job) context.Context {
	return context.WithValue(ctx, jobMetadataKey{}, JobMetadata{ID: job.ID, Attempt: job.Attempts})
}

// JobIDFromContext retrieves a job identifier previously attached to context.
func JobIDFromContext(ctx context.Context) types.JobID {
	metadata, ok := JobMetadataFromContext(ctx)
	if ok {
		return metadata.ID
	}
	return types.JobIDNil
}

// JobMetadataFromContext retrieves immutable metadata for the active attempt.
func JobMetadataFromContext(ctx context.Context) (JobMetadata, bool) {
	if ctx == nil {
		return JobMetadata{}, false
	}
	metadata, ok := ctx.Value(jobMetadataKey{}).(JobMetadata)
	if !ok || metadata.ID == types.JobIDNil || metadata.Attempt <= 0 {
		return JobMetadata{}, false
	}
	return metadata, true
}
