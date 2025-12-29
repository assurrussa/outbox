package models

import (
	"time"

	"github.com/assurrussa/outbox/shared/types"
)

type JobFailed struct {
	ID         types.JobID `json:"id" db:"id"`
	JobID      types.JobID `json:"jobId" db:"job_id"`
	Queue      string      `json:"queue" db:"queue"`
	Name       string      `json:"name" db:"name"`
	Payload    string      `json:"payload" db:"payload"`
	Reason     string      `json:"reason" db:"reason"`
	FailedAt   time.Time   `json:"failedAt" db:"failed_at"`
	CreatedAt  time.Time   `json:"createdAt" db:"created_at"`
	Connection string      `json:"connection" db:"connection"`
	Exception  string      `json:"exception" db:"exception"`
}
