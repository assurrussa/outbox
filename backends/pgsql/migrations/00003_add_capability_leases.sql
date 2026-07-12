-- +goose Up
-- +goose StatementBegin
alter table jobs
    add column schema_version integer not null default 1
        constraint jobs_schema_version_positive check (schema_version > 0),
    add column lease_token uuid not null default '00000000-0000-0000-0000-000000000000';

alter table jobs_failed
    add column schema_version integer not null default 1
        constraint jobs_failed_schema_version_positive check (schema_version > 0);

create index jobs_capability_claim_index
    on jobs (name, schema_version, available_at, reserved_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop index if exists jobs_capability_claim_index;

alter table jobs_failed
    drop column if exists schema_version;

alter table jobs
    drop column if exists lease_token,
    drop column if exists schema_version;
-- +goose StatementEnd
