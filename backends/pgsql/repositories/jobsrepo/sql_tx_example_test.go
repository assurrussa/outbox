package jobsrepo_test

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"

	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
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
	// A missing transaction fails before any database I/O. In the host, supply
	// its existing transaction and leave commit or rollback to that owner.
	fmt.Println(stage(context.Background(), nil, "message-17", time.Time{}))
	// Output: outbox PostgreSQL transaction is required
}
