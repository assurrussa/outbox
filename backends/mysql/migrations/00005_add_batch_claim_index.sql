-- +goose Up
-- +goose StatementBegin
ALTER TABLE jobs
    DROP INDEX jobs_capability_claim_index,
    ADD INDEX jobs_capability_claim_index
        (name, schema_version, available_at, created_at, id, reserved_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE jobs
    DROP INDEX jobs_capability_claim_index,
    ADD INDEX jobs_capability_claim_index
        (name, schema_version, available_at, reserved_at);
-- +goose StatementEnd
