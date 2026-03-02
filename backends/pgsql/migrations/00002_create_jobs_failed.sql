-- +goose Up
-- +goose StatementBegin
create table if not exists jobs_failed
(
    id         uuid primary key,
    job_id     uuid                      not null
        constraint jobs_failed_uuid_unique unique,
    connection text                      not null,
    queue      text                      not null,
    name       text                      not null,
    payload    text                      not null,
    reason     text                      not null,
    exception  text                      not null,
    failed_at  TIMESTAMPTZ default now() not null,
    created_at TIMESTAMPTZ default now() not null
);

create index jobs_failed_queue_index on jobs_failed (queue);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table jobs_failed;
-- +goose StatementEnd
