package pgsqlclient_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
)

type testTracer struct{ id int }

func (*testTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (*testTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestQueryTracerPoolConfiguration(t *testing.T) {
	first, second := &testTracer{id: 1}, &testTracer{id: 2}
	combined := multitracer.New(first, second)
	cases := []struct {
		name    string
		options []pgsqlclient.OptPoolOptionsSetter
		want    pgx.QueryTracer
	}{
		{name: "disabled by default"},
		{name: "enabled", options: []pgsqlclient.OptPoolOptionsSetter{pgsqlclient.WithQueryTracer(first)}, want: first},
		{name: "composed", options: []pgsqlclient.OptPoolOptionsSetter{pgsqlclient.WithQueryTracer(combined)}, want: combined},
		{
			name: "last option wins",
			options: []pgsqlclient.OptPoolOptionsSetter{
				pgsqlclient.WithQueryTracer(first),
				pgsqlclient.WithQueryTracer(second),
			}, want: second,
		},
		{
			name: "disabled explicitly",
			options: []pgsqlclient.OptPoolOptionsSetter{
				pgsqlclient.WithQueryTracer(first),
				pgsqlclient.WithQueryTracer(nil),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := append([]pgsqlclient.OptPoolOptionsSetter{
				pgsqlclient.WithCheck(false), pgsqlclient.WithMinConnectionsCount(1),
			}, tc.options...)
			client, err := pgsqlclient.NewPool(t.Context(),
				pgsqlclient.NewOptions("localhost:1", "unused", "unused", "unused", options...))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			if tc.want == nil {
				require.Nil(t, client.Pool().Config().ConnConfig.Tracer)
			} else {
				require.Same(t, tc.want, client.Pool().Config().ConnConfig.Tracer)
			}
		})
	}
}
