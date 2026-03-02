-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs_failed (
    id CHAR(36) NOT NULL,
    job_id CHAR(36) NOT NULL,
    connection TEXT NOT NULL,
    queue VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    payload LONGTEXT NOT NULL,
    reason TEXT NOT NULL,
    exception TEXT NOT NULL,
    failed_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY jobs_failed_job_id_unique (job_id),
    INDEX jobs_failed_queue_index (queue)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jobs_failed;
-- +goose StatementEnd
