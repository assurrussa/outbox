-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs (
    id CHAR(36) NOT NULL,
    queue VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    payload LONGTEXT NOT NULL,
    attempts INT NOT NULL,
    reserved_at DATETIME(6) NULL,
    available_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    INDEX jobs_queue_index (queue),
    INDEX jobs_available_at_index (available_at),
    INDEX jobs_reserved_at_index (reserved_at)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jobs;
-- +goose StatementEnd
