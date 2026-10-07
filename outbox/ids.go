package outbox

import "github.com/assurrussa/outbox/shared/types"

// JobID identifies a persisted job. The alias preserves compatibility with
// existing repository implementations and the ID's text and SQL encoding.
type JobID = types.JobID

// MessageID identifies a fan-out event or delivery. It is distinct from JobID.
type MessageID = types.MessageID

// NewJobID returns a new random job identifier.
func NewJobID() JobID { return types.NewJobID() }

// NewMessageID returns a new random message identifier.
func NewMessageID() MessageID { return types.NewMessageID() }

// ParseJobID parses a UUID using the existing ID encoding. A zero UUID is
// parseable; call Validate when a nonzero identifier is required.
func ParseJobID(value string) (JobID, error) { return types.Parse[JobID](value) }

// ParseMessageID parses a UUID using the existing ID encoding. A zero UUID is
// parseable; call Validate when a nonzero identifier is required.
func ParseMessageID(value string) (MessageID, error) { return types.Parse[MessageID](value) }
