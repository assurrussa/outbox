package sqlite

import (
	"time"

	"github.com/assurrussa/outbox/shared/types"
)

const (
	// DefaultPageSize is used by ListPage when limit is not positive.
	DefaultPageSize = 10
	// MaxPageSize bounds a ListPage request. Larger limits return an error.
	MaxPageSize = 1000
)

// PageCursor is the exclusive boundary for ListPage's created_at DESC, id DESC
// ordering. Copy both fields from the last returned row, without rounding its
// timestamp. A nil cursor starts at the newest row. For DLQ rows ID is the failed
// row's ID, not its JobID. Cursors belong to one backend and one table; they are
// not portable between databases or between the active queue and the DLQ.
//
// This structural alias keeps the pagination API compatible across backend
// modules without requiring a new core dependency.
type PageCursor = struct {
	CreatedAt time.Time
	ID        types.JobID
}
