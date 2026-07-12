-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs ADD COLUMN deduplication_key TEXT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS jobs_deduplication_key_unique
    ON jobs (deduplication_key)
    WHERE deduplication_key IS NOT NULL;

CREATE TABLE outbox_job_idempotency_keys (
    deduplication_key TEXT PRIMARY KEY,
    job_id TEXT NOT NULL UNIQUE,
    fingerprint TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS outbox_job_idempotency_keys_created_at_index
    ON outbox_job_idempotency_keys (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS jobs_deduplication_key_unique;
DROP TABLE IF EXISTS outbox_job_idempotency_keys;
ALTER TABLE jobs DROP COLUMN deduplication_key;
-- +goose StatementEnd
