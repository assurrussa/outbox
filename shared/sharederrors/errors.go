package sharederrors

import "errors"

var (
	ErrNoJobs         = errors.New("no jobs found")
	ErrJobStatNotInit = errors.New("jobsStatRepo not initialized")
)
