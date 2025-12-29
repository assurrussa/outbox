package transport

import (
	"context"
	"io"
	"net/http"

	transporthttp "github.com/assurrussa/outbox/infrastructure/transport/http"
)

//go:generate toolsmocks

type HTTPClient interface {
	DoWithRequestAndParse(ctx context.Context, request transporthttp.Request, data any) error
	DoWithRequest(ctx context.Context, request transporthttp.Request) (*http.Response, error)
	Do(
		ctx context.Context,
		method string,
		url string,
		body io.Reader,
		headers transporthttp.RequestHeaders,
	) (*http.Response, error)
}
