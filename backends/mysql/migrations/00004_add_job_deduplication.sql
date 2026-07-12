-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs
    ADD COLUMN deduplication_key VARCHAR(512) CHARACTER SET ascii NULL;

CREATE UNIQUE INDEX jobs_deduplication_key_unique
    ON jobs (deduplication_key);

CREATE TABLE outbox_job_idempotency_keys (
    deduplication_key VARCHAR(512) CHARACTER SET ascii NOT NULL,
    job_id CHAR(36) NOT NULL,
    fingerprint CHAR(64) CHARACTER SET ascii NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (deduplication_key),
    UNIQUE KEY outbox_job_idempotency_keys_job_id_unique (job_id),
    INDEX outbox_job_idempotency_keys_created_at_index (created_at)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX jobs_deduplication_key_unique ON jobs;
DROP TABLE IF EXISTS outbox_job_idempotency_keys;
ALTER TABLE jobs DROP COLUMN deduplication_key;
-- +goose StatementEnd
