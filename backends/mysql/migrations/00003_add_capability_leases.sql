-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs
    ADD COLUMN schema_version INT NOT NULL DEFAULT 1,
    ADD COLUMN lease_token CHAR(36) NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    ADD CONSTRAINT jobs_schema_version_positive CHECK (schema_version > 0);

ALTER TABLE jobs_failed
    ADD COLUMN schema_version INT NOT NULL DEFAULT 1,
    ADD CONSTRAINT jobs_failed_schema_version_positive CHECK (schema_version > 0);

CREATE INDEX jobs_capability_claim_index
    ON jobs (name, schema_version, available_at, reserved_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX jobs_capability_claim_index ON jobs;

ALTER TABLE jobs_failed
    DROP CHECK jobs_failed_schema_version_positive,
    DROP COLUMN schema_version;

ALTER TABLE jobs
    DROP CHECK jobs_schema_version_positive,
    DROP COLUMN lease_token,
    DROP COLUMN schema_version;
-- +goose StatementEnd
