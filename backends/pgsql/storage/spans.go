package storage

import (
	"context"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"
)

func CreateSpan(ctx context.Context, operationName, query string, args []any) (context.Context, func()) {
	span, ctx := opentracing.StartSpanFromContext(ctx, "postgres."+operationName)

	span.LogFields(
		log.String("query", query),
		log.Object("args", args),
	)

	return ctx, func() {
		span.Finish()
	}
}
