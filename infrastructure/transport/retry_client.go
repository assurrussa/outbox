package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/avast/retry-go"

	transporthttp "github.com/assurrussa/outbox/infrastructure/transport/http"
)

type RetryClient struct {
	retryOpts []retry.Option
	client    HTTPClient
}

func NewRetryClient(client HTTPClient, retryOpts ...retry.Option) *RetryClient {
	return &RetryClient{
		client:    client,
		retryOpts: retryOpts,
	}
}

func (c *RetryClient) DoWithRequestAndParse(
	ctx context.Context,
	request transporthttp.Request,
	data any,
) error {
	err := retry.Do(func() error {
		return c.client.DoWithRequestAndParse(ctx, request, data)
	}, append(c.retryOpts, retry.Context(ctx))...)
	if err != nil {
		return fmt.Errorf("retry do: %w", err)
	}

	return nil
}

func (c *RetryClient) DoWithRequest(
	ctx context.Context,
	request transporthttp.Request,
) (*http.Response, error) {
	return c.doRetry(ctx, func() (*http.Response, error) {
		return c.client.DoWithRequest(ctx, request)
	})
}

func (c *RetryClient) Do(
	ctx context.Context,
	method string,
	url string,
	body io.Reader,
	headers transporthttp.RequestHeaders,
) (*http.Response, error) {
	return c.doRetry(ctx, func() (*http.Response, error) {
		return c.client.Do(ctx, method, url, body, headers)
	})
}

func (c *RetryClient) doRetry(
	ctx context.Context,
	callback func() (*http.Response, error),
) (*http.Response, error) {
	var resp *http.Response
	err := retry.Do(func() error {
		//nolint:bodyclose // it's valid
		r, err := callback()
		if err != nil {
			return fmt.Errorf("do retry: %w", err)
		}

		resp = r

		return nil
	}, append(c.retryOpts, retry.Context(ctx))...)
	if err != nil {
		return nil, fmt.Errorf("retry do: %w", err)
	}

	return resp, nil
}
