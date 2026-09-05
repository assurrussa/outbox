-- +goose Up
-- +goose StatementBegin
UPDATE outbox_job_idempotency_keys AS k
JOIN jobs AS j ON j.id = k.job_id
SET k.deduplication_key = j.deduplication_key
WHERE j.deduplication_key IS NOT NULL
  AND CAST(j.deduplication_key AS BINARY) <> CAST(k.deduplication_key AS BINARY);

ALTER TABLE outbox_job_idempotency_keys
    MODIFY COLUMN deduplication_key VARBINARY(512) NOT NULL;

ALTER TABLE jobs
    MODIFY COLUMN deduplication_key VARBINARY(512) NULL,
    MODIFY COLUMN name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL;

ALTER TABLE jobs_failed
    MODIFY COLUMN name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SIGNAL SQLSTATE '45000'
    SET MESSAGE_TEXT = 'Exact identifier migration cannot be reversed; retain the schema when rolling back the application';
-- +goose StatementEnd
