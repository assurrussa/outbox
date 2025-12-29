-- pico.UP
CREATE TABLE IF NOT EXISTS outbox_jobs_failed (
      id UUID NOT NULL,
      job_id UUID NOT NULL,
      queue TEXT NOT NULL,
      name TEXT NOT NULL,
      payload TEXT NOT NULL,
      reason TEXT NOT NULL,
      failed_at DATETIME NOT NULL,
      created_at DATETIME NOT NULL,
      connection TEXT,
      exception TEXT,
      PRIMARY KEY (id)
) USING memtx DISTRIBUTED BY (id)
OPTION (TIMEOUT = 3.0);

-- pico.DOWN
DROP TABLE IF EXISTS "outbox_jobs_failed";
