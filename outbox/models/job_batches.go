package models

import (
	"database/sql"
	"time"
)

type JobBatches struct {
	ID           int64        `json:"id" db:"id"`
	Name         string       `json:"name" db:"name"`
	TotalJobs    int64        `json:"totalJobs" db:"total_jobs"`
	PendingJobs  int64        `json:"pendingJobs" db:"pending_jobs"`
	FailedJobs   int64        `json:"failedJobs" db:"failed_jobs"`
	FailedJobIDs string       `json:"failedJobIds" db:"failed_job_ids"`
	Options      string       `json:"options" db:"options"`
	CancelledAt  sql.NullTime `json:"cancelledAt" db:"cancelled_at"`
	CreatedAt    time.Time    `json:"createdAt" db:"created_at"`
	FinishedAt   sql.NullTime `json:"finishedAt" db:"finished_at"`
}
