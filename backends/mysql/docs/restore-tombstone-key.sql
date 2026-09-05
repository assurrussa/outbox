-- Run before migration 00006, with writers and workers stopped.
-- Bind: original key, job ID, expected fingerprint, observed key, original key.
-- Commit the caller-owned transaction only when exactly one row changed.
UPDATE outbox_job_idempotency_keys AS k
SET deduplication_key = ?
WHERE k.job_id = ?
  AND CAST(k.fingerprint AS BINARY) = CAST(? AS BINARY)
  AND CAST(k.deduplication_key AS BINARY) = CAST(? AS BINARY)
  AND CAST(k.deduplication_key AS BINARY) <> CAST(? AS BINARY)
  AND NOT EXISTS (SELECT 1 FROM jobs AS j WHERE j.id = k.job_id);
