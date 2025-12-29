package validator_test

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/assurrussa/outbox/shared/validator"
)

type options struct {
	DB      *sql.DB      `validate:"required"`
	Handler http.Handler `validate:"required"`
	Size    string       `validate:"parse-size"`
}

func TestValidate_TrickyNils(t *testing.T) {
	cases := []struct {
		in      options
		wantErr bool
	}{
		// Negative.
		{
			in:      options{DB: nil, Handler: new(handlerMock), Size: "4KB"},
			wantErr: true,
		},
		{
			in:      options{DB: new(sql.DB), Handler: http.HandlerFunc(nil), Size: "4KB"},
			wantErr: true,
		},
		{
			in:      options{DB: new(sql.DB), Handler: (*handlerMock)(nil), Size: "4KB"},
			wantErr: true,
		},
		{
			in:      options{DB: new(sql.DB), Handler: (*handlerMock)(nil), Size: "0"},
			wantErr: true,
		},
		{
			in:      options{DB: new(sql.DB), Handler: (*handlerMock)(nil), Size: ""},
			wantErr: true,
		},
		{
			in:      options{DB: new(sql.DB), Handler: (*handlerMock)(nil), Size: "b"},
			wantErr: true,
		},

		// Positive.
		{
			in:      options{DB: new(sql.DB), Handler: new(handlerMock), Size: "4KB"},
			wantErr: false,
		},
		{
			in: options{
				DB:      new(sql.DB),
				Handler: http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}),
				Size:    "4KB",
			},
			wantErr: false,
		},
	}

	for _, tt := range cases {
		t.Run("", func(t *testing.T) {
			err := validator.Validator.Struct(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRegisterValidate(t *testing.T) {
	assert.Panics(t, func() {
		validator.MustRegisterValidation("test", nil)
	})
}

var _ http.Handler = (*handlerMock)(nil)

type handlerMock struct{}

func (h *handlerMock) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {
}
