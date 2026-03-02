-- +goose Up
-- +goose StatementBegin
create table if not exists jobs
(
    id           uuid primary key,
    queue        varchar(255) not null,
    name         varchar(255) not null,
    payload      text         not null,
    attempts     smallint     not null,
    reserved_at  TIMESTAMPTZ  null,
    available_at TIMESTAMPTZ  not null,
    created_at   TIMESTAMPTZ  not null DEFAULT NOW()
);

create index jobs_queue_index on jobs (queue);
create index jobs_available_at_index on jobs (available_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table jobs;
-- +goose StatementEnd
