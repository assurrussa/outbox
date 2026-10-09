package jobsrepo_test

import (
	"context"
	"database/sql"
	"time"

	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	coreoutbox "github.com/assurrussa/outbox/outbox"
)

func ExampleNewSQLTxPutter() {
	// The host calls stage from its existing business/Inbox transaction.
	stage := func(ctx context.Context, tx *sql.Tx, eventID string, at time.Time) error {
		producer, err := jobsrepo.NewSQLTxPutter(tx)
		if err != nil {
			return err
		}
		var putter coreoutbox.UniqueVersionedPutter = producer
		_, err = putter.PutVersionedUnique(ctx, eventID, "message.accepted", 1, "payload", at)
		return err
	}
	_ = stage
	// Only the host commits or rolls back. A staging result cannot confirm commit.
	// Output:
}
