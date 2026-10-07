package outbox

import (
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
)

// ErrRetryPolicy means retry scheduling failed without finalizing the claim.
var ErrRetryPolicy = errors.New("outbox invalid retry policy")

// RetryAttempt describes a counted handler failure eligible for another attempt.
// Attempt is the persisted one-based attempt, not a retry counter. Payload and
// lease state are intentionally excluded.
type RetryAttempt struct {
	JobID      JobID
	Capability JobCapability
	Attempt    int
	Err        error
}

// RetryPolicy schedules ordinary counted failures independently of leases.
// RetryAt, Permanent, DeferAt and exhausted attempts take precedence. Top-level
// batch errors defer without consuming an attempt and do not call the policy.
// Implementations must promptly return and be safe for concurrent workers.
// A zero delay permits immediate retry; a negative delay fails closed.
type RetryPolicy interface {
	RetryDelay(attempt RetryAttempt) time.Duration
}

// RetryPolicyFunc adapts a function to RetryPolicy.
type RetryPolicyFunc func(attempt RetryAttempt) time.Duration

func (f RetryPolicyFunc) RetryDelay(attempt RetryAttempt) time.Duration { return f(attempt) }

type exponentialRetryPolicy struct {
	baseDelay time.Duration
	maxDelay  time.Duration
	jitter    func(time.Duration) time.Duration
}

// NewExponentialRetryPolicy doubles baseDelay for each persisted attempt after
// the first, capped at maxDelay. Both must be positive and baseDelay <= maxDelay.
// Optional jitter receives the capped delay; its result is capped again. It must
// be concurrency-safe and promptly return. Nil jitter makes delays deterministic.
// Negative jitter results and panics fail closed during scheduling.
func NewExponentialRetryPolicy(
	baseDelay, maxDelay time.Duration,
	jitter func(time.Duration) time.Duration,
) (RetryPolicy, error) {
	if baseDelay <= 0 || maxDelay <= 0 || baseDelay > maxDelay {
		return nil, fmt.Errorf("%w: require 0 < base delay <= maximum delay", ErrRetryPolicy)
	}
	return exponentialRetryPolicy{baseDelay: baseDelay, maxDelay: maxDelay, jitter: jitter}, nil
}

func (p exponentialRetryPolicy) RetryDelay(attempt RetryAttempt) time.Duration {
	delay := p.baseDelay
	for step := 1; step < attempt.Attempt && delay < p.maxDelay; step++ {
		if delay > p.maxDelay/2 {
			delay = p.maxDelay
			break
		}
		delay *= 2
	}
	if p.jitter != nil {
		delay = p.jitter(delay)
	}
	return min(delay, p.maxDelay)
}

// retryTime uses the caller's completion clock. It never reads reserveFor or
// alters attempt accounting. Single-job legacy lease recovery bypasses it.
func (s *Service) retryTime(job models.Job, handleErr error, now time.Time, legacyDelay time.Duration) (time.Time, error) {
	now = now.UTC()
	if at, explicit := RetryTime(handleErr); explicit {
		if at.Before(now) {
			return now, nil
		}
		return at.UTC(), nil
	}
	delay := legacyDelay
	if s.retryPolicy != nil {
		var err error
		delay, err = retryPolicyDelay(s.retryPolicy, RetryAttempt{
			JobID:      job.ID,
			Capability: JobCapability{Name: job.Name, SchemaVersion: normalizeSchemaVersion(job.SchemaVersion)},
			Attempt:    job.Attempts,
			Err:        handleErr,
		})
		if err != nil {
			return time.Time{}, err
		}
	}
	return now.Add(delay), nil
}

func retryPolicyDelay(policy RetryPolicy, attempt RetryAttempt) (delay time.Duration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: panic: %v\nstack:\n%s", ErrRetryPolicy, recovered, debug.Stack())
		}
	}()
	delay = policy.RetryDelay(attempt)
	if delay < 0 {
		return 0, fmt.Errorf("%w: negative delay %s", ErrRetryPolicy, delay)
	}
	return delay, nil
}
