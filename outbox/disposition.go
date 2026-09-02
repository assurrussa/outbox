package outbox

import (
	"errors"
	"fmt"
	"time"
)

type permanentError struct {
	err error
}

func (e permanentError) Error() string { return fmt.Sprintf("permanent: %v", e.err) }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks a handler failure as non-retryable. A nil error remains nil.
func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err contains a Permanent disposition.
func IsPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

type retryAtError struct {
	err error
	at  time.Time
}

func (e retryAtError) Error() string {
	return fmt.Sprintf("retry at %s: %v", e.at.UTC().Format(time.RFC3339Nano), e.err)
}

func (e retryAtError) Unwrap() error { return e.err }

// RetryAt marks a handler failure for a persisted retry no earlier than at. A
// nil error remains nil. The timestamp is normalized to UTC.
func RetryAt(err error, at time.Time) error {
	if err == nil {
		return nil
	}
	return retryAtError{err: err, at: at.UTC()}
}

// RetryTime returns the requested persisted retry time when err contains a
// RetryAt disposition.
func RetryTime(err error) (time.Time, bool) {
	var target retryAtError
	if !errors.As(err, &target) {
		return time.Time{}, false
	}
	return target.at, true
}

type deferAtError struct {
	err error
	at  time.Time
}

func (e deferAtError) Error() string {
	return fmt.Sprintf("defer at %s: %v", e.at.UTC().Format(time.RFC3339Nano), e.err)
}

func (e deferAtError) Unwrap() error { return e.err }

// DeferAt postpones a job without consuming an attempt. A nil error remains
// nil and the timestamp is normalized to UTC.
func DeferAt(err error, at time.Time) error {
	if err == nil {
		return nil
	}
	return deferAtError{err: err, at: at.UTC()}
}

// DeferTime returns the requested no-attempt defer time when err contains a
// DeferAt disposition.
func DeferTime(err error) (time.Time, bool) {
	var target deferAtError
	if !errors.As(err, &target) {
		return time.Time{}, false
	}
	return target.at, true
}
