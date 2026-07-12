-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version > 0);
ALTER TABLE jobs ADD COLUMN lease_token TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
ALTER TABLE jobs_failed ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version > 0);

CREATE INDEX IF NOT EXISTS jobs_capability_claim_index
    ON jobs (name, schema_version, available_at, reserved_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS jobs_capability_claim_index;
ALTER TABLE jobs_failed DROP COLUMN schema_version;
ALTER TABLE jobs DROP COLUMN lease_token;
ALTER TABLE jobs DROP COLUMN schema_version;
-- +goose StatementEnd
