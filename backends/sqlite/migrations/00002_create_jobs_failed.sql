-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs_failed (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL UNIQUE,
    connection TEXT NOT NULL,
    queue TEXT NOT NULL,
    name TEXT NOT NULL,
    payload TEXT NOT NULL,
    reason TEXT NOT NULL,
    exception TEXT NOT NULL,
    failed_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS jobs_failed_queue_index ON jobs_failed (queue);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jobs_failed;
-- +goose StatementEnd
