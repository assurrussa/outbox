-- +goose Up
-- +goose StatementBegin
create table outbox_job_replays
(
    request_id                 uuid primary key,
    failed_job_id              uuid not null references jobs_failed (id) on delete restrict,
    source_job_id              uuid not null,
    job_id                     uuid not null unique,
    business_key               text not null check (length(btrim(business_key)) > 0),
    queue                      text not null,
    name                       text not null,
    schema_version             integer not null check (schema_version > 0),
    payload                    text not null,
    original_deduplication_key text not null,
    created_at                 timestamptz not null default now()
);

-- No jobs foreign key: acknowledgement deletes the queue row, not provenance.
create index outbox_job_replays_failed_job_index on outbox_job_replays (failed_job_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Acquire the destructive lock before reading emptiness. An in-flight first
-- replay must commit/roll back before this transaction decides whether to drop.
lock table outbox_job_replays in access exclusive mode;
do $$
begin
    if exists (select 1 from outbox_job_replays) then
        raise exception 'replay provenance exists; explicit host retention decision required';
    end if;
end $$;
drop table outbox_job_replays;
-- +goose StatementEnd
