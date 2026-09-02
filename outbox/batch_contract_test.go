//nolint:testpackage // Exact-cover validation is deliberately package-local.
package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/assurrussa/outbox/shared/types"
)

func TestBatchConfigZeroValueAndSingletonControl(t *testing.T) {
	defaults, err := (BatchConfig{}).Normalize()
	if err != nil {
		t.Fatalf("normalize zero: %v", err)
	}
	if defaults.MaxMessages != 100 || defaults.MaxBytes != 4<<20 || defaults.MaxWait != 25*time.Millisecond {
		t.Fatalf("defaults = %#v", defaults)
	}
	singleton, err := (BatchConfig{MaxMessages: 1}).Normalize()
	if err != nil || singleton.MaxMessages != 1 || singleton.MaxBytes != defaults.MaxBytes || singleton.MaxWait != defaults.MaxWait {
		t.Fatalf("singleton = %#v, %v", singleton, err)
	}
	for _, invalid := range []BatchConfig{
		{MaxMessages: -1},
		{MaxBytes: -1},
		{MaxWait: -1},
		{MaxMessages: MaxReservationBatchSize + 1},
	} {
		if _, err := invalid.Normalize(); err == nil {
			t.Fatalf("Normalize(%#v) succeeded", invalid)
		}
	}
}

func TestValidateBatchResultRequiresExactKeyCover(t *testing.T) {
	first, second := types.NewJobID(), types.NewJobID()
	input := []BatchJobItem{{JobID: first}, {JobID: second}}
	wantErr := errors.New("retry")
	errs, err := validateBatchResult(input, BatchResult{Items: []BatchItemResult{
		{JobID: second, Err: wantErr}, {JobID: first},
	}})
	if err != nil || len(errs) != 2 || errs[0] != nil || !errors.Is(errs[1], wantErr) {
		t.Fatalf("valid result = %#v, %v", errs, err)
	}

	tests := []struct {
		name   string
		result BatchResult
	}{
		{name: "missing", result: BatchResult{Items: []BatchItemResult{{JobID: first}}}},
		{name: "duplicate", result: BatchResult{Items: []BatchItemResult{{JobID: first}, {JobID: first}}}},
		{name: "unknown", result: BatchResult{Items: []BatchItemResult{{JobID: first}, {JobID: types.NewJobID()}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateBatchResult(input, test.result); !errors.Is(err, ErrInvalidBatchResult) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDeferAtPreservesCauseAndUTCInstant(t *testing.T) {
	cause := errors.New("broker unavailable")
	wanted := time.Date(2030, 1, 2, 3, 4, 5, 6, time.FixedZone("offset", 3*60*60))
	err := DeferAt(cause, wanted)
	got, ok := DeferTime(errors.Join(errors.New("wrapped"), err))
	if !ok || !got.Equal(wanted) || got.Location() != time.UTC || !errors.Is(err, cause) {
		t.Fatalf("defer = %s/%v, error=%v", got, ok, err)
	}
	if DeferAt(nil, wanted) != nil {
		t.Fatal("DeferAt(nil) must remain nil")
	}
}
