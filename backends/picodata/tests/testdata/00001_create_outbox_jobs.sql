-- pico.UP
CREATE TABLE IF NOT EXISTS outbox_jobs (
   id UUID NOT NULL,
   queue TEXT NOT NULL,
   name TEXT NOT NULL,
   payload TEXT NOT NULL,
   attempts INTEGER NOT NULL,
   reserved_at DATETIME,
   available_at DATETIME NOT NULL,
   created_at DATETIME NOT NULL,
   PRIMARY KEY (id)
) USING memtx DISTRIBUTED BY (id)
OPTION (TIMEOUT = 3.0);

-- pico.DOWN
DROP TABLE IF EXISTS "outbox_jobs";
