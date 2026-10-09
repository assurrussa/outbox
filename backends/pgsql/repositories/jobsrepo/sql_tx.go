package jobsrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

var _ coreoutbox.UniqueVersionedPutter = (*SQLTxPutter)(nil)

// SQLTxPutter stages unique PostgreSQL jobs in one caller-owned database/sql
// transaction. It has no pool, fallback connection or transaction manager.
// The zero value is not usable; construct it with NewSQLTxPutter.
type SQLTxPutter struct {
	tx *sql.Tx
}

// NewSQLTxPutter binds a producer to an existing PostgreSQL transaction.
// It performs no I/O and never begins, commits or rolls back a transaction.
// The caller must use the intended database and search_path with the Outbox
// migrations applied, and must not change that schema binding while staging.
// A non-nil transaction's driver and state are checked by PostgreSQL and
// database/sql when PutVersionedUnique executes.
func NewSQLTxPutter(tx *sql.Tx) (*SQLTxPutter, error) {
	if tx == nil {
		return nil, errors.New("outbox PostgreSQL transaction is required")
	}
	return &SQLTxPutter{tx: tx}, nil
}

// PutVersionedUnique stages one immutable identity using the same SQL and
// fingerprint as Repo.CreateJobVersionedUniqueResult. The returned result is
// provisional until the caller confirms Commit; rollback removes both the job
// and its idempotency key. Propagate errors to the transaction owner.
// Reusing this putter after commit or rollback returns a wrapped sql.ErrTxDone.
// An ambiguous commit must be reconciled by retrying the same identity/content,
// never by treating this staging result as proof of durable delivery.
func (p *SQLTxPutter) PutVersionedUnique(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (coreoutbox.UniquePutResult, error) {
	if p == nil || p.tx == nil {
		return coreoutbox.UniquePutResult{}, errors.New("outbox PostgreSQL transaction is required")
	}
	result, err := createUniqueJob(
		deduplicationKey, name, schemaVersion, payload, availableAt,
		func(query string, args ...any) (types.JobID, error) {
			var jobID types.JobID
			err := p.tx.QueryRowContext(ctx, query, args...).Scan(&jobID)
			return jobID, err
		},
	)
	if err != nil {
		return coreoutbox.UniquePutResult{}, fmt.Errorf("stage unique job in caller transaction: %w", err)
	}
	return result, nil
}
