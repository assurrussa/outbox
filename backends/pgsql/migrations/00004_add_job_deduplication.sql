-- +goose Up
-- +goose StatementBegin
alter table jobs
    add column deduplication_key text null;

create unique index jobs_deduplication_key_unique
    on jobs (deduplication_key)
    where deduplication_key is not null;

create table outbox_job_idempotency_keys
(
    deduplication_key text primary key,
    job_id              uuid        not null unique,
    fingerprint         text        not null,
    created_at          TIMESTAMPTZ not null default now()
);

create index outbox_job_idempotency_keys_created_at_index
    on outbox_job_idempotency_keys (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop index if exists jobs_deduplication_key_unique;

drop table if exists outbox_job_idempotency_keys;

alter table jobs
    drop column if exists deduplication_key;
-- +goose StatementEnd
