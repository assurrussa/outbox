package sharedjob

import (
	coreoutbox "github.com/assurrussa/outbox/outbox"
)

// DefaultJob is retained for backwards compatibility.
//
// Deprecated: use outbox.DefaultJob instead.
type DefaultJob = coreoutbox.DefaultJob
