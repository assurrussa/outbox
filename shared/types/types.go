package types //nolint:revive // it's valid name

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	ErrJobIDUuidZero     = errors.New("JobID uuid is zero")
	ErrMessageIDUuidZero = errors.New("MessageID uuid is zero")
	JobIDNil             = JobID(uuid.Nil)
	MessageIDNil         = MessageID(uuid.Nil)
)

type JobID uuid.UUID                             //
func NewJobID() JobID                            { return JobID(uuid.New()) }
func (t JobID) String() string                   { return uuid.UUID(t).String() }
func (t JobID) Value() (driver.Value, error)     { return t.String(), nil }
func (t *JobID) Scan(src any) error              { return (*uuid.UUID)(t).Scan(src) }
func (t JobID) MarshalText() ([]byte, error)     { return uuid.UUID(t).MarshalText() }
func (t *JobID) UnmarshalText(data []byte) error { return (*uuid.UUID)(t).UnmarshalText(data) }
func (t JobID) IsZero() bool                     { return t == JobIDNil }
func (t JobID) Matches(x any) bool {
	v, ok := x.(JobID)
	if !ok {
		return false
	}

	return t == v
}

func (t JobID) Validate() error {
	if t.IsZero() {
		return fmt.Errorf("validate: %w", ErrJobIDUuidZero)
	}
	return nil
}

func (t JobID) AsPointer() *JobID {
	if t.IsZero() {
		return nil
	}
	return &t
}

type MessageID uuid.UUID           //
func NewMessageID() MessageID      { return MessageID(uuid.New()) }
func (t MessageID) String() string { return uuid.UUID(t).String() }
func (t MessageID) Value() (driver.Value, error) {
	return t.String(), nil
}
func (t *MessageID) Scan(src any) error              { return (*uuid.UUID)(t).Scan(src) }
func (t MessageID) MarshalText() ([]byte, error)     { return uuid.UUID(t).MarshalText() }
func (t *MessageID) UnmarshalText(data []byte) error { return (*uuid.UUID)(t).UnmarshalText(data) }
func (t MessageID) IsZero() bool                     { return t == MessageIDNil }
func (t MessageID) Matches(x any) bool {
	v, ok := x.(MessageID)
	if !ok {
		return false
	}

	return t == v
}

func (t MessageID) Validate() error {
	if t.IsZero() {
		return fmt.Errorf("validate: %w", ErrMessageIDUuidZero)
	}
	return nil
}

func (t MessageID) AsPointer() *MessageID {
	if t.IsZero() {
		return nil
	}
	return &t
}

type TypeSet = interface {
	JobID | MessageID
}

func Parse[T TypeSet](s string) (T, error) {
	v, err := uuid.Parse(s)
	return T(v), err
}

func MustParse[T TypeSet](s string) T {
	return T(uuid.MustParse(s))
}
