package transporthttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/goccy/go-json"
)

type Client struct {
	Client    *http.Client
	unmarshal JSONUnmarshaler
}

func NewClient(opts ...Option) (*Client, error) {
	options := &Options{
		timeout:   30 * time.Second,
		unmarshal: json.Unmarshal,
	}

	for _, opt := range opts {
		opt(options)
	}

	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("validate options: %w", err)
	}

	client := options.client
	if client == nil {
		client = &http.Client{
			Timeout: options.timeout,
		}
	}

	return &Client{
		Client:    client,
		unmarshal: options.unmarshal,
	}, nil
}

func (c *Client) DoWithRequestAndParse(ctx context.Context, request Request, data any) error {
	resp, err := c.DoWithRequest(ctx, request)
	if err != nil {
		return fmt.Errorf("DoWithRequest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if request.ExpectStatusCode != 0 && resp.StatusCode != request.ExpectStatusCode {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("wrong request: %w", ErrNotFound)
		}

		if request.ReadResponseAlways {
			body, err := io.ReadAll(resp.Body)

			return fmt.Errorf("wrong request: %w, reason: %d, %s: body: [%s]",
				errors.Join(ErrReadResponse, err), resp.StatusCode, resp.Status, body,
			)
		}

		return fmt.Errorf("wrong request: %w, reason: %d, %s", ErrReadResponse, resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("io ReadAll: %w, %w", ErrReadResponse, err)
	}

	err = c.unmarshal(body, data)
	if err != nil {
		return fmt.Errorf("json Unmarshal: %w, %w", ErrReadResponse, err)
	}

	return nil
}

func (c *Client) DoWithRequest(ctx context.Context, request Request) (*http.Response, error) {
	return c.Do(ctx, request.Method, request.URL, request.Body, request.Headers)
}

func (c *Client) Do(
	ctx context.Context,
	method string,
	url string,
	body io.Reader,
	headers RequestHeaders,
) (*http.Response, error) {
	return doRequest(ctx, c.Client, headers, method, url, body)
}

// doRequest HTTP запрос.
func doRequest(
	ctx context.Context,
	client *http.Client,
	headers RequestHeaders,
	method string,
	url string,
	body io.Reader,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("can't do new request: %w, %w", ErrCreateRequest, err)
	}

	for key, val := range headers {
		req.Header.Set(key, val)
	}

	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("can't do send request: %w, %w", ErrSendRequest, err)
	}

	if resp == nil {
		return nil, errors.New("request succeeded but response is nil")
	}

	if resp.Body == nil {
		return resp, nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	return resp, nil
}
