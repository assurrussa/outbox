# Upgrade to exact MySQL identifiers

Migration 00006 repairs the registry spelling from each **active** job before
converting keys to `VARBINARY(512)`. A completed job can no longer supply its
original spelling. On schema v5, a batch replay could replace `EventA` with
`eventa` in the registry while returning the original job ID. If that job was
ACKed before upgrade, only `eventa` survives. After upgrade, replaying `EventA`
can create a new job and repeat an already completed effect.

This is different from a message historically suppressed by the old
case-insensitive comparison. Neither the job ID nor the content fingerprint
can recover an original key: the fingerprint does not contain the key. Ordinary
completed tombstones are expected and their presence does not prove corruption.

## Before applying 00006

Stop writers and workers and keep them stopped through reconciliation and the
complete migration. Retain a database backup and a trusted producer/audit journal
covering the host's replay retention. Adapt table names for host-owned schemas.

List active rows whose key spelling differs from the registry; 00006 repairs
these from the active job:

```sql
SELECT j.id AS job_id,
       HEX(j.deduplication_key) AS active_key_hex,
       HEX(k.deduplication_key) AS registry_key_hex,
       k.fingerprint
FROM jobs AS j
JOIN outbox_job_idempotency_keys AS k ON k.job_id = j.id
WHERE j.deduplication_key IS NOT NULL
  AND CAST(j.deduplication_key AS BINARY)
      <> CAST(k.deduplication_key AS BINARY)
ORDER BY j.id;
```

Enumerate tombstones for external comparison in bounded pages:

```sql
SELECT k.job_id, HEX(k.deduplication_key) AS registry_key_hex,
       k.fingerprint, k.created_at
FROM outbox_job_idempotency_keys AS k
WHERE k.job_id > ?
  AND NOT EXISTS (SELECT 1 FROM jobs AS j WHERE j.id = k.job_id)
ORDER BY k.job_id
LIMIT 1000;
```

Bind an empty string for the first cursor, then the last returned `job_id` for
each following page. Complete the inventory before making repairs. Compare the
exact bytes against a trusted mapping of original key, job ID and content
fingerprint; casing or trailing spaces must not be normalized. A journal that
only records the later replay is insufficient. Do not infer that every listed
tombstone needs repair.

## Restore a confirmed original key

Use [the parameterized repair statement](restore-tombstone-key.sql) on one
pinned connection inside an explicit transaction. Bind, in order:

1. The original key verified from the external journal.
2. Its original job ID.
3. The verified fingerprint.
4. The currently observed key, preserving its exact bytes.
5. The same original key as parameter 1, to reject a no-op update.

Use a valid original ASCII key of 1–512 bytes, as required by unique puts on
schema v5. Decode the inventory's HEX value for parameter 4. The statement
requires matching ID, fingerprint and current key bytes, and excludes active
jobs. It changes only the key, retaining the job ID, fingerprint, creation time
and retention history.

Read affected rows from that update. Commit only when exactly one row changed.
Roll back on an error or any other count; a zero count means the assumptions no
longer match or the row is already correct. A uniqueness conflict requires
reconciliation of both records with the journal, not deletion of either record.
Re-read the repaired row on the same connection before committing.

Without an authoritative original-key mapping, this procedure cannot guarantee
deduplication of historical original spellings across the upgrade. If that
guarantee is required, keep the upgrade paused until the host resolves the
history. The migration does not automatically detect such lost information or
block every completed tombstone.

## Apply and verify

Apply 00006 completely, then verify repaired keys and unchanged IDs/fingerprints
before resuming writers and workers. An identical replay with the restored
original key and original content must return its old job ID with
`Created == false`; different exact keys remain independent.

MySQL commits DDL per statement. Resume an interrupted upgrade while writers
and workers remain stopped. `Down` is prohibited; an application rollback keeps
the exact schema. Historical aliases and suppressed messages are not recreated.
