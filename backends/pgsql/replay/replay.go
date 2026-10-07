// Package replay stages admitted ordinary PostgreSQL DLQ jobs without changing
// their business operation. Hosts own authorization, exact handler support and
// business-effect idempotency. Fan-out and other backends are unsupported.
package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assurrussa/outbox/backends/pgsql"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
)

// JobID is compatible with repository and core job IDs, without a shared import
// in consumer code. RequestID identifies one immutable replay request.
type (
	JobID     = types.JobID
	RequestID = types.MessageID
)

// NewRequestID generates one random request identity; persist it before replay.
func NewRequestID() RequestID { return types.NewMessageID() }

// ParseRequestID decodes UUID text. Replay rejects the zero UUID.
func ParseRequestID(value string) (RequestID, error) { return types.Parse[RequestID](value) }

// ParseJobID decodes UUID text for failed-row and queue-job identifiers.
func ParseJobID(value string) (JobID, error) { return types.Parse[JobID](value) }

var (
	ErrNotConfigured     = errors.New("replay requires a PostgreSQL client")
	ErrInvalidRequest    = errors.New("replay requires nonzero request and failed job IDs")
	ErrAdmissionRequired = errors.New("replay requires host admission")
	ErrNotAdmitted       = errors.New("replay admission requires a stable business key")
	ErrUnsupportedFanout = errors.New("fan-out replay is unsupported")
	ErrNestedTransaction = errors.New("replay must own its transaction")
	ErrSourceNotFound    = errors.New("replay source not found")
	ErrInvalidSource     = errors.New("invalid replay source")
	ErrSourceActive      = errors.New("original job or previous replay is still active")
	ErrRequestConflict   = errors.New("replay request conflicts with retained provenance")
	ErrReplayNotFound    = errors.New("replay provenance not found")
)

// Request identifies one intentional replay of a jobs_failed row.
type Request struct {
	RequestID   RequestID
	FailedJobID JobID
}

// Source is the original failed operation. OriginalDeduplicationKey is a hint
// from a retained enqueue tombstone, not proof of handler effect idempotency.
type Source struct {
	FailedJobID              JobID
	OriginalJobID            JobID
	Capability               outbox.JobCapability
	Payload                  string
	Queue                    string
	OriginalDeduplicationKey string
}

// Admission must authorize the request, confirm exact capability support and
// return the stable business-effect key already consumed by the handler. It runs
// on every call, including repeats, inside a transaction holding the failed row.
// Keep it bounded and read-only; do not perform business effects here.
type Admission func(context.Context, Source) (string, error)

// Record retains the replay's source, business key and fresh delivery identity.
type Record struct {
	RequestID   RequestID
	JobID       JobID
	Source      Source
	BusinessKey string
	CreatedAt   time.Time
}

// Result is confirmed only after commit. Created=false means this same request
// was already recorded; it never recreates an acknowledged job.
type Result struct {
	Record  Record
	Created bool
}

// Replayer stages ordinary jobs in one borrowed PostgreSQL database/schema.
type Replayer struct {
	db   storage.DBEngine
	jobs *jobsrepo.Repo
}

// New borrows the client; the caller retains its lifetime and schema ownership.
// Apply the backend's embedded migrations before using replay.
func New(client pgsql.Client) (*Replayer, error) {
	if client == nil || client.DB() == nil {
		return nil, ErrNotConfigured
	}
	jobs, err := jobsrepo.New(jobsrepo.NewOptions(client))
	if err != nil {
		return nil, fmt.Errorf("replay jobs repository: %w", err)
	}
	return &Replayer{db: client.DB(), jobs: jobs}, nil
}

// Replay copies the exact payload/capability/queue into fresh, unleased work.
// The failed row, original enqueue key and business identity remain unchanged.
// An error (including uncertain commit) returns zero Result; retry the SAME
// request ID to resolve uncertainty. Never substitute a new business key.
func (r *Replayer) Replay(ctx context.Context, request Request, admit Admission) (Result, error) {
	if r == nil || r.db == nil || r.jobs == nil {
		return Result{}, ErrNotConfigured
	}
	if request.RequestID.IsZero() || request.FailedJobID.IsZero() {
		return Result{}, ErrInvalidRequest
	}
	if admit == nil {
		return Result{}, ErrAdmissionRequired
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if storage.GetTx(ctx) != nil {
		return Result{}, ErrNestedTransaction
	}
	var result Result
	err := transaction.New(r.db).ReadCommitted(ctx, pgx.ReadWrite, func(txCtx context.Context) error {
		var err error
		result, err = r.replay(txCtx, request, admit)
		return err
	})
	if err != nil {
		return Result{}, fmt.Errorf("replay: %w", err)
	}
	return result, nil
}

func (r *Replayer) replay(ctx context.Context, request Request, admit Admission) (Result, error) {
	source, err := r.lockSource(ctx, request.FailedJobID)
	if err != nil {
		return Result{}, err
	}
	if err := validateSource(source); err != nil {
		return Result{}, err
	}
	prior, err := r.ByRequestID(ctx, request.RequestID)
	if err == nil {
		// Tombstone pruning may remove the original key hint. Use the retained
		// snapshot for admission; compare the operation's immutable fields.
		source.OriginalDeduplicationKey = prior.Source.OriginalDeduplicationKey
		if source != prior.Source {
			return Result{}, ErrRequestConflict
		}
		key, admissionErr := admit(ctx, prior.Source)
		if admissionErr != nil {
			return Result{}, fmt.Errorf("host admission: %w", admissionErr)
		}
		if key != prior.BusinessKey {
			return Result{}, ErrRequestConflict
		}
		return Result{Record: prior}, nil
	}
	if !errors.Is(err, ErrReplayNotFound) {
		return Result{}, err
	}
	key, err := admit(ctx, source)
	if err != nil {
		return Result{}, fmt.Errorf("host admission: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		return Result{}, ErrNotAdmitted
	}
	var active bool
	err = r.db.QueryRow(ctx, "replay.active", `select exists (
		select 1 from jobs where id=$1 or id in (
			select job_id from outbox_job_replays where failed_job_id=$2
		)
	)`, source.OriginalJobID, source.FailedJobID).Scan(&active)
	if err != nil {
		return Result{}, fmt.Errorf("check active source: %w", err)
	}
	if active {
		return Result{}, ErrSourceActive
	}
	return r.stage(ctx, request.RequestID, source, key)
}

func validateSource(source Source) error {
	if source.OriginalJobID.IsZero() || source.Capability.Validate() != nil {
		return ErrInvalidSource
	}
	if source.Capability.Name == outbox.FanoutDispatcherJobName || strings.HasPrefix(source.Capability.Name, "fanout.") {
		return ErrUnsupportedFanout
	}
	return nil
}

func (r *Replayer) lockSource(ctx context.Context, id JobID) (Source, error) {
	var source Source
	err := r.db.QueryRow(ctx, "replay.source", `select
		f.id, f.job_id, f.name, f.schema_version, f.payload, f.queue,
		coalesce((select deduplication_key from outbox_job_idempotency_keys where job_id=f.job_id), '')
		from jobs_failed f where f.id=$1 for update of f`, id).Scan(
		&source.FailedJobID, &source.OriginalJobID, &source.Capability.Name,
		&source.Capability.SchemaVersion, &source.Payload, &source.Queue, &source.OriginalDeduplicationKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrSourceNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("lock failed job: %w", err)
	}
	return source, nil
}

func (r *Replayer) stage(ctx context.Context, id RequestID, source Source, key string) (Result, error) {
	now := time.Now().UTC()
	job, err := r.jobs.CreateJobVersionedUniqueResult(ctx, "outbox.replay."+id.String(),
		source.Capability.Name, source.Capability.SchemaVersion, source.Payload, now)
	if errors.Is(err, outbox.ErrIdempotencyConflict) || (err == nil && !job.Created) {
		return Result{}, ErrRequestConflict
	}
	if err != nil {
		return Result{}, fmt.Errorf("stage job: %w", err)
	}
	tag, err := r.db.Exec(ctx, "replay.queue", "update jobs set queue=$1 where id=$2", source.Queue, job.JobID)
	if err != nil {
		return Result{}, fmt.Errorf("copy queue: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return Result{}, ErrRequestConflict
	}
	var createdAt time.Time
	err = r.db.QueryRow(ctx, "replay.record", `insert into outbox_job_replays (
		request_id, failed_job_id, source_job_id, job_id, business_key, queue,
		name, schema_version, payload, original_deduplication_key
	) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning created_at`,
		id, source.FailedJobID, source.OriginalJobID, job.JobID, key, source.Queue,
		source.Capability.Name, source.Capability.SchemaVersion, source.Payload, source.OriginalDeduplicationKey,
	).Scan(&createdAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "outbox_job_replays_pkey" {
			return Result{}, errors.Join(ErrRequestConflict, err)
		}
		return Result{}, fmt.Errorf("retain replay provenance: %w", err)
	}
	return Result{Created: true, Record: Record{
		RequestID: id, JobID: job.JobID, Source: source, BusinessKey: key, CreatedAt: createdAt,
	}}, nil
}

// ByRequestID reads retained provenance. The host owns authorization for reads.
func (r *Replayer) ByRequestID(ctx context.Context, id RequestID) (Record, error) {
	if id.IsZero() {
		return Record{}, ErrInvalidRequest
	}
	return r.record(ctx, "request_id", id)
}

// ByJobID resolves a fresh queue identity to its original operation, including
// after ACK. ErrReplayNotFound means the job has no replay provenance.
func (r *Replayer) ByJobID(ctx context.Context, id JobID) (Record, error) {
	if id.IsZero() {
		return Record{}, ErrInvalidRequest
	}
	return r.record(ctx, "job_id", id)
}

func (r *Replayer) record(ctx context.Context, column string, id any) (Record, error) {
	if r == nil || r.db == nil {
		return Record{}, ErrNotConfigured
	}
	var record Record
	// column is selected only by the two fixed internal callers above.
	err := r.db.QueryRow(ctx, "replay.lookup", `select
		request_id, job_id, failed_job_id, source_job_id, business_key, queue,
		name, schema_version, payload, original_deduplication_key, created_at
		from outbox_job_replays where `+column+`=$1`, id).Scan(
		&record.RequestID, &record.JobID, &record.Source.FailedJobID, &record.Source.OriginalJobID,
		&record.BusinessKey, &record.Source.Queue, &record.Source.Capability.Name,
		&record.Source.Capability.SchemaVersion, &record.Source.Payload,
		&record.Source.OriginalDeduplicationKey, &record.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrReplayNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read replay provenance: %w", err)
	}
	return record, nil
}
