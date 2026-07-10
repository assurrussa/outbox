package models

import (
	"database/sql"
	"time"

	"github.com/assurrussa/outbox/shared/types"
)

type Job struct {
	ID            types.JobID         `json:"id" db:"id"`
	Queue         string              `json:"queue" db:"queue"`
	Name          string              `json:"name" db:"name"`
	SchemaVersion types.SchemaVersion `json:"schemaVersion" db:"schema_version"`
	Payload       string              `json:"payload" db:"payload"`
	Attempts      int                 `json:"attempts" db:"attempts"`
	ReservedAt    sql.NullTime        `json:"reservedAt" db:"reserved_at"`
	LeaseToken    types.LeaseToken    `json:"-" db:"lease_token"`
	AvailableAt   time.Time           `json:"availableAt" db:"available_at"`
	CreatedAt     time.Time           `json:"createdAt" db:"created_at"`
}
