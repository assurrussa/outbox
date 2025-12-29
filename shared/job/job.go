package sharedjob

import (
	"time"
)

const (
	defaultExecutionTimeout = 30 * time.Second
	defaultMaxAttempts      = 30
)

// DefaultJob is useful for embedding into other repo.
type DefaultJob struct{}

func (j DefaultJob) ExecutionTimeout() time.Duration {
	return defaultExecutionTimeout
}

func (j DefaultJob) MaxAttempts() int {
	return defaultMaxAttempts
}
