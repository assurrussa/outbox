package storage_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql/storage"
	"github.com/assurrussa/outbox/outbox/logger"
)

func TestCreate_InvalidOptions(t *testing.T) {
	ctx := context.Background()

	_, err := storage.Create(ctx, "")
	require.Error(t, err)
}

func TestCreate_OptionsValidation(t *testing.T) {
	dsn := "root:pass@tcp(localhost:3306)/db"

	tests := []struct {
		name    string
		options []storage.Option
		wantErr bool
	}{
		{
			name: "nil logger",
			options: []storage.Option{
				storage.WithLogger(nil),
				storage.WithCheckPing(false),
			},
			wantErr: true,
		},
		{
			name: "invalid max open conns",
			options: []storage.Option{
				storage.WithMaxOpenConns(0),
				storage.WithCheckPing(false),
			},
			wantErr: true,
		},
		{
			name: "invalid max idle conns",
			options: []storage.Option{
				storage.WithMaxIdleConns(-1),
				storage.WithCheckPing(false),
			},
			wantErr: true,
		},
		{
			name: "valid options",
			options: []storage.Option{
				storage.WithLogger(logger.Default()),
				storage.WithMaxOpenConns(1),
				storage.WithMaxIdleConns(0),
				storage.WithCheckPing(false),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := storage.Create(context.Background(), dsn, tt.options...)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, client)
			require.NoError(t, client.Close())
		})
	}
}
