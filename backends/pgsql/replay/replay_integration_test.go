//go:build integration

package replay_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/migrator"
	"github.com/assurrussa/outbox/backends/pgsql/replay"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
)

type replayFixture struct {
	pool     *pgxpool.Pool
	sqlDB    *sql.DB
	client   borrowedClient
	replayer *replay.Replayer
	jobs     *jobsrepo.Repo
}

func openReplayFixture(t *testing.T) replayFixture {
	t.Helper()
	dsn := os.Getenv("OUTBOX_PG_DSN")
	if dsn == "" {
		t.Skip("set OUTBOX_PG_DSN to an owned disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := "outbox_replay_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "create schema "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, dropErr := admin.Exec(cleanupCtx, "drop schema "+quoted+" cascade")
		require.NoError(t, dropErr)
	})
	config, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, migrator.RunEmbedded(ctx, db, logger.Discard(), migrator.WithCommand("up")))
	client := borrowedClient{engine: pgsqlclient.NewDBEngine(pool, "test", logger.Discard())}
	r, err := replay.New(client)
	require.NoError(t, err)
	jobs, err := jobsrepo.New(jobsrepo.NewOptions(client))
	require.NoError(t, err)
	return replayFixture{pool: pool, sqlDB: db, client: client, replayer: r, jobs: jobs}
}

func (f replayFixture) source(t *testing.T, name string) replay.Source {
	t.Helper()
	failedID, err := replay.ParseJobID(uuid.NewString())
	require.NoError(t, err)
	originalID, err := replay.ParseJobID(uuid.NewString())
	require.NoError(t, err)
	source := replay.Source{
		FailedJobID: failedID, OriginalJobID: originalID,
		Capability: outbox.JobCapability{Name: name, SchemaVersion: 2},
		Payload:    `{"business_key":"order-17","unknown_field":"preserve"}`,
		Queue:      "original-queue", OriginalDeduplicationKey: "original." + originalID.String(),
	}
	_, err = f.pool.Exec(t.Context(), `insert into jobs_failed
		(id,job_id,connection,queue,name,schema_version,payload,reason,exception)
		values ($1,$2,'pgsql',$3,$4,$5,$6,'original reason','original exception')`,
		source.FailedJobID, source.OriginalJobID, source.Queue, source.Capability.Name,
		source.Capability.SchemaVersion, source.Payload)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `insert into outbox_job_idempotency_keys
		(deduplication_key,job_id,fingerprint) values ($1,$2,'original fingerprint')`,
		source.OriginalDeduplicationKey, source.OriginalJobID)
	require.NoError(t, err)
	return source
}

func requestFor(source replay.Source) replay.Request {
	return replay.Request{RequestID: replay.NewRequestID(), FailedJobID: source.FailedJobID}
}

func admitOriginal(_ context.Context, source replay.Source) (string, error) {
	if source.Capability != (outbox.JobCapability{Name: "order.created", SchemaVersion: 2}) {
		return "", errUnsupportedHandler
	}
	// The host's actual handler consumes this key from this unchanged payload.
	if source.Payload != `{"business_key":"order-17","unknown_field":"preserve"}` {
		return "", errUnsupportedHandler
	}
	return "order-17", nil
}

var (
	errUnsupportedHandler = errors.New("host lacks exact supported handler")
	errCommitFault        = errors.New("injected commit response failure")
)

func (f replayFixture) absent(t *testing.T, request replay.Request) {
	t.Helper()
	var keys, records, jobs int
	require.NoError(t, f.pool.QueryRow(t.Context(), `select
		(select count(*) from outbox_job_idempotency_keys where deduplication_key=$1),
		(select count(*) from outbox_job_replays where request_id=$2),
		(select count(*) from jobs where deduplication_key=$1)`,
		"outbox.replay."+request.RequestID.String(), request.RequestID).Scan(&keys, &records, &jobs))
	require.Zero(t, keys)
	require.Zero(t, records)
	require.Zero(t, jobs)
}

func (f replayFixture) ack(t *testing.T, record replay.Record) {
	t.Helper()
	now := time.Now().UTC().Add(time.Second)
	token := types.NewLeaseToken()
	jobs, err := f.jobs.FindAndReserveJobsForCapability(
		t.Context(), now, now.Add(time.Minute), token, record.Source.Capability, 1000)
	require.NoError(t, err)
	found := false
	for _, job := range jobs {
		if job.ID != record.JobID {
			continue
		}
		found = true
		count, deleteErr := f.jobs.DeleteJobWithLease(t.Context(), job.ID, token, now)
		require.NoError(t, deleteErr)
		require.EqualValues(t, 1, count)
	}
	require.True(t, found)
}

func TestReplayPostgreSQLContract(t *testing.T) {
	f := openReplayFixture(t)
	t.Run("fresh work preserved evidence and repeat after ACK", func(t *testing.T) { testReplayEvidence(t, f) })
	t.Run("admission and unsupported inputs leave no work", func(t *testing.T) { testReplayAdmission(t, f) })
	t.Run("active original and prior replay reject new requests", func(t *testing.T) { testReplayActiveSource(t, f) })
	t.Run("request conflict and repeat admission", func(t *testing.T) { testReplayConflict(t, f) })
	t.Run("same request concurrent callers stage once", func(t *testing.T) { testReplayConcurrency(t, f) })
	t.Run("different concurrent requests fail closed", func(t *testing.T) { testReplayConcurrentConflict(t, f) })
	t.Run("persistence failures roll back work key and provenance", func(t *testing.T) { testReplayRollback(t, f) })
	t.Run("failed and ambiguous commit return zero confirmed result", func(t *testing.T) { testReplayCommit(t, f) })
}

func testReplayEvidence(t *testing.T, f replayFixture) {
	t.Helper()
	source := f.source(t, "order.created")
	request := requestFor(source)
	result, err := f.replayer.Replay(t.Context(), request, admitOriginal)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, source, result.Record.Source)
	require.Equal(t, "order-17", result.Record.BusinessKey)
	require.NotEqual(t, source.OriginalJobID, result.Record.JobID)
	require.False(t, result.Record.JobID.IsZero())
	job, err := f.jobs.GetByID(t.Context(), result.Record.JobID)
	require.NoError(t, err)
	require.Equal(t, source.Payload, job.Payload)
	require.Equal(t, source.Queue, job.Queue)
	require.Equal(t, source.Capability.Name, job.Name)
	require.Equal(t, source.Capability.SchemaVersion, job.SchemaVersion)
	require.Zero(t, job.Attempts)
	require.False(t, job.ReservedAt.Valid)
	require.True(t, job.LeaseToken.IsZero())
	var reason, exception, fingerprint string
	require.NoError(t, f.pool.QueryRow(t.Context(), `select f.reason,f.exception,k.fingerprint
		from jobs_failed f join outbox_job_idempotency_keys k on k.job_id=f.job_id where f.id=$1`,
		source.FailedJobID).Scan(&reason, &exception, &fingerprint))
	require.Equal(t, "original reason", reason)
	require.Equal(t, "original exception", exception)
	require.Equal(t, "original fingerprint", fingerprint)
	for _, acknowledged := range []bool{false, true} {
		if acknowledged {
			f.ack(t, result.Record)
		}
		repeat, repeatErr := f.replayer.Replay(t.Context(), request, admitOriginal)
		require.NoError(t, repeatErr)
		require.False(t, repeat.Created)
		require.Equal(t, result.Record, repeat.Record)
	}
	byJob, err := f.replayer.ByJobID(t.Context(), result.Record.JobID)
	require.NoError(t, err)
	require.Equal(t, result.Record, byJob)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), "select count(*) from jobs where id=$1", result.Record.JobID).Scan(&count))
	require.Zero(t, count)
	_, err = f.pool.Exec(t.Context(), "delete from jobs_failed where id=$1", source.FailedJobID)
	require.Error(t, err, "replayed failed evidence cannot be deleted")
	require.Error(t, migrator.RunEmbedded(t.Context(), f.sqlDB, logger.Discard(), migrator.WithCommand("down")))
	_, err = f.replayer.ByRequestID(t.Context(), request.RequestID)
	require.NoError(t, err, "guarded Down retained provenance")
}

func testReplayAdmission(t *testing.T, f replayFixture) {
	t.Helper()
	for _, name := range []string{"fanout.topic.kind", outbox.FanoutDispatcherJobName, "unsupported"} {
		source := f.source(t, name)
		request := requestFor(source)
		result, err := f.replayer.Replay(t.Context(), request, admitOriginal)
		if name == "unsupported" {
			require.ErrorIs(t, err, errUnsupportedHandler)
		} else {
			require.ErrorIs(t, err, replay.ErrUnsupportedFanout)
		}
		require.Zero(t, result)
		f.absent(t, request)
	}
	source := f.source(t, "order.created")
	unsupported := requestFor(source)
	_, err := f.pool.Exec(t.Context(), "update jobs_failed set schema_version=3 where id=$1", source.FailedJobID)
	require.NoError(t, err)
	result, err := f.replayer.Replay(t.Context(), unsupported, admitOriginal)
	require.ErrorIs(t, err, errUnsupportedHandler)
	require.Zero(t, result)
	f.absent(t, unsupported)
	_, err = f.pool.Exec(t.Context(), "update jobs_failed set schema_version=2 where id=$1", source.FailedJobID)
	require.NoError(t, err)
	for _, admit := range []replay.Admission{
		func(context.Context, replay.Source) (string, error) { return " \t", nil },
		func(context.Context, replay.Source) (string, error) { panic("host admission panic") },
	} {
		request := requestFor(source)
		result, err := f.replayer.Replay(t.Context(), request, admit)
		require.Error(t, err)
		require.Zero(t, result)
		f.absent(t, request)
	}
	ctx, cancel := context.WithCancel(t.Context())
	request := requestFor(source)
	result, err = f.replayer.Replay(ctx, request, func(ctx context.Context, _ replay.Source) (string, error) {
		cancel()
		return "order-17", ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, result)
	f.absent(t, request)
	missing := requestFor(source)
	missing.FailedJobID = source.OriginalJobID
	result, err = f.replayer.Replay(t.Context(), missing, admitOriginal)
	require.ErrorIs(t, err, replay.ErrSourceNotFound)
	require.Zero(t, result)
	f.absent(t, missing)
}

func testReplayActiveSource(t *testing.T, f replayFixture) {
	t.Helper()
	source := f.source(t, "order.created")
	_, err := f.pool.Exec(t.Context(), `insert into jobs (
		id,name,schema_version,queue,payload,attempts,available_at,reserved_at,lease_token)
		values ($1,$2,$3,$4,$5,4,now()+interval '1 day',now()+interval '1 day',$6)`, source.OriginalJobID, source.Capability.Name,
		source.Capability.SchemaVersion, source.Queue, source.Payload, types.NewLeaseToken())
	require.NoError(t, err)
	request := requestFor(source)
	result, err := f.replayer.Replay(t.Context(), request, admitOriginal)
	require.ErrorIs(t, err, replay.ErrSourceActive)
	require.Zero(t, result)
	f.absent(t, request)
	_, err = f.pool.Exec(t.Context(), "delete from jobs where id=$1", source.OriginalJobID)
	require.NoError(t, err)
	result, err = f.replayer.Replay(t.Context(), request, admitOriginal)
	require.NoError(t, err)
	next := requestFor(source)
	rejected, err := f.replayer.Replay(t.Context(), next, admitOriginal)
	require.ErrorIs(t, err, replay.ErrSourceActive)
	require.Zero(t, rejected)
	f.absent(t, next)
	f.ack(t, result.Record)
	// A distinct admitted request is allowed after ACK, with the same effect
	// key. It is an intentional retry, never an implicit request-ID change.
	nextResult, err := f.replayer.Replay(t.Context(), next, admitOriginal)
	require.NoError(t, err)
	require.True(t, nextResult.Created)
	require.NotEqual(t, result.Record.JobID, nextResult.Record.JobID)
	require.Equal(t, result.Record.BusinessKey, nextResult.Record.BusinessKey)
	f.ack(t, nextResult.Record)
}

func testReplayConflict(t *testing.T, f replayFixture) {
	t.Helper()
	source := f.source(t, "order.created")
	request := requestFor(source)
	result, err := f.replayer.Replay(t.Context(), request, admitOriginal)
	require.NoError(t, err)
	other := f.source(t, "order.created")
	conflict := request
	conflict.FailedJobID = other.FailedJobID
	rejected, err := f.replayer.Replay(t.Context(), conflict, admitOriginal)
	require.ErrorIs(t, err, replay.ErrRequestConflict)
	require.Zero(t, rejected)
	rejected, err = f.replayer.Replay(t.Context(), request, func(context.Context, replay.Source) (string, error) {
		return "new-effect", nil
	})
	require.ErrorIs(t, err, replay.ErrRequestConflict)
	require.Zero(t, rejected)
	rejected, err = f.replayer.Replay(t.Context(), request, func(context.Context, replay.Source) (string, error) {
		return "", errUnsupportedHandler
	})
	require.ErrorIs(t, err, errUnsupportedHandler)
	require.Zero(t, rejected)
	_, err = f.pool.Exec(t.Context(), "update jobs_failed set payload='altered' where id=$1", source.FailedJobID)
	require.NoError(t, err)
	rejected, err = f.replayer.Replay(t.Context(), request, admitOriginal)
	require.ErrorIs(t, err, replay.ErrRequestConflict)
	require.Zero(t, rejected)
	_, err = f.pool.Exec(t.Context(), "update jobs_failed set payload=$1 where id=$2", source.Payload, source.FailedJobID)
	require.NoError(t, err)
	f.ack(t, result.Record)
	// Retained original-key hint remains usable if the host prunes its old
	// ordinary enqueue tombstone. Journal request identity still survives.
	_, err = f.pool.Exec(t.Context(), "delete from outbox_job_idempotency_keys where job_id=$1 or job_id=$2",
		source.OriginalJobID, result.Record.JobID)
	require.NoError(t, err)
	repeat, err := f.replayer.Replay(t.Context(), request, admitOriginal)
	require.NoError(t, err)
	require.Equal(t, result.Record, repeat.Record)
}

func testReplayConcurrency(t *testing.T, f replayFixture) {
	t.Helper()
	source := f.source(t, "order.created")
	request := requestFor(source)
	var results [2]replay.Result
	var errs [2]error
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range results {
		group.Go(func() { <-start; results[i], errs[i] = f.replayer.Replay(t.Context(), request, admitOriginal) })
	}
	close(start)
	group.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, results[0].Record, results[1].Record)
	require.NotEqual(t, results[0].Created, results[1].Created)
	f.ack(t, results[0].Record)
}

func testReplayConcurrentConflict(t *testing.T, f replayFixture) {
	t.Helper()
	for _, sameSource := range []bool{true, false} {
		source := f.source(t, "order.created")
		requests := [2]replay.Request{requestFor(source), requestFor(source)}
		want := replay.ErrSourceActive
		if !sameSource {
			other := f.source(t, "order.created")
			requests[1] = replay.Request{RequestID: requests[0].RequestID, FailedJobID: other.FailedJobID}
			want = replay.ErrRequestConflict
		}
		var results [2]replay.Result
		var errs [2]error
		start := make(chan struct{})
		var group sync.WaitGroup
		for i := range requests {
			group.Go(func() { <-start; results[i], errs[i] = f.replayer.Replay(t.Context(), requests[i], admitOriginal) })
		}
		close(start)
		group.Wait()
		winner, loser := 0, 1
		if errs[winner] != nil {
			winner, loser = loser, winner
		}
		require.NoError(t, errs[winner])
		require.True(t, results[winner].Created)
		require.ErrorIs(t, errs[loser], want)
		require.Zero(t, results[loser])
		if sameSource {
			f.absent(t, requests[loser])
		}
		f.ack(t, results[winner].Record)
	}
}

func testReplayRollback(t *testing.T, f replayFixture) {
	t.Helper()
	for _, fault := range []struct{ table, check string }{
		{"jobs", "payload <> '{\"business_key\":\"order-17\",\"unknown_field\":\"preserve\"}'"},
		{"jobs", "queue <> 'original-queue'"},
		{"outbox_job_replays", "payload <> '{\"business_key\":\"order-17\",\"unknown_field\":\"preserve\"}'"},
	} {
		source := f.source(t, "order.created")
		request := requestFor(source)
		_, err := f.pool.Exec(t.Context(), "alter table "+fault.table+" add constraint replay_fault check ("+fault.check+") not valid")
		require.NoError(t, err)
		result, err := f.replayer.Replay(t.Context(), request, admitOriginal)
		require.Error(t, err)
		require.Zero(t, result)
		f.absent(t, request)
		_, err = f.pool.Exec(t.Context(), "alter table "+fault.table+" drop constraint replay_fault")
		require.NoError(t, err)
	}
}

func testReplayCommit(t *testing.T, f replayFixture) {
	t.Helper()
	for _, afterCommit := range []bool{false, true} {
		source := f.source(t, "order.created")
		request := requestFor(source)
		client := borrowedClient{engine: commitFaultEngine{DBEngine: f.client.engine, afterCommit: afterCommit}}
		r, err := replay.New(client)
		require.NoError(t, err)
		result, err := r.Replay(t.Context(), request, admitOriginal)
		require.ErrorIs(t, err, errCommitFault)
		require.Zero(t, result)
		if !afterCommit {
			f.absent(t, request)
		}
		resolved, err := f.replayer.Replay(t.Context(), request, admitOriginal)
		require.NoError(t, err)
		require.Equal(t, !afterCommit, resolved.Created)
		f.ack(t, resolved.Record)
	}
}

type commitFaultEngine struct {
	storage.DBEngine
	afterCommit bool
}

func (e commitFaultEngine) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := e.DBEngine.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return commitFaultTx{Tx: tx, afterCommit: e.afterCommit}, nil
}

type commitFaultTx struct {
	pgx.Tx
	afterCommit bool
}

func (t commitFaultTx) Commit(ctx context.Context) error {
	if t.afterCommit {
		if err := t.Tx.Commit(ctx); err != nil {
			return err
		}
	}
	return errCommitFault
}
