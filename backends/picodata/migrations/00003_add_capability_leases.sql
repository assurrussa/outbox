-- pico.UP
ALTER TABLE outbox_jobs
    ADD COLUMN schema_version INTEGER WAIT APPLIED GLOBALLY;

UPDATE outbox_jobs SET schema_version = 1 WHERE schema_version IS NULL;

ALTER TABLE outbox_jobs
    ADD COLUMN lease_token UUID WAIT APPLIED GLOBALLY;

UPDATE outbox_jobs
SET lease_token = '00000000-0000-0000-0000-000000000000'
WHERE lease_token IS NULL;

ALTER TABLE outbox_jobs_failed
    ADD COLUMN schema_version INTEGER WAIT APPLIED GLOBALLY;

UPDATE outbox_jobs_failed SET schema_version = 1 WHERE schema_version IS NULL;

-- pico.DOWN
UPDATE outbox_jobs SET schema_version = 1 WHERE schema_version IS NULL;
