package transporthttp_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transporthttp "github.com/assurrussa/outbox/infrastructure/transport/http"
)

type testData struct {
	Val string `json:"val"`
	URL string `json:"url"`
}

func Test_CreateError(t *testing.T) {
	tests := []struct {
		name string
		opts []transporthttp.Option
	}{
		{
			name: "nil unmarshal",
			opts: []transporthttp.Option{transporthttp.WithUnmarshal(nil)},
		},
		{
			name: "timeout too small",
			opts: []transporthttp.Option{transporthttp.WithTimeout(100 * time.Millisecond)},
		},
		{
			name: "timeout too large",
			opts: []transporthttp.Option{transporthttp.WithTimeout(10 * time.Minute)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := transporthttp.NewClient(tt.opts...)
			require.Error(t, err)
			assert.Nil(t, c)
		})
	}
}

func Test_DoWithRequestAndParse_NotFound(t *testing.T) {
	server, c := CreateServer(t, http.StatusNotFound, func(t *testing.T, _ string, _ string) []byte {
		t.Helper()

		return []byte("not found")
	})
	defer server.Close()
	c.Client = server.Client()

	req := transporthttp.Request{
		Method:           http.MethodGet,
		URL:              server.URL,
		ExpectStatusCode: http.StatusOK,
	}
	var body testData
	err := c.DoWithRequestAndParse(t.Context(), req, &body)
	require.ErrorIs(t, err, transporthttp.ErrNotFound)
}

func Test_DoWithRequestAndParse_RequestError(t *testing.T) {
	c, err := transporthttp.NewClient()
	require.NoError(t, err)

	req := transporthttp.Request{
		Method: "INVALID METHOD",
		URL:    "://invalid-url",
	}
	var body testData
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DoWithRequest")
}

func Test_Do_ClientError(t *testing.T) {
	c, err := transporthttp.NewClient()
	require.NoError(t, err)

	expectedErr := errors.New("network error")
	c.Client.Transport = &errorTransport{err: expectedErr}

	req := transporthttp.Request{
		Method: http.MethodGet,
		URL:    "http://example.com",
	}
	//nolint:bodyclose // it's resp nil and valid
	resp, err := c.DoWithRequest(t.Context(), req)
	require.ErrorIs(t, err, transporthttp.ErrSendRequest)
	assert.Nil(t, resp)
}

func Test_Do_HTTPClientError(t *testing.T) {
	expectedErr := errors.New("network error")
	client := &http.Client{
		Transport: &errorTransport{err: expectedErr},
	}
	c, err := transporthttp.NewClient(transporthttp.WithClient(client))
	require.NoError(t, err)

	//nolint:bodyclose // it's resp nil and valid
	resp, err := c.Do(t.Context(), http.MethodGet, "http://example.com", nil, nil)
	require.ErrorIs(t, err, transporthttp.ErrSendRequest)
	assert.Nil(t, resp)
}

func Test_Do_BodyReadError(t *testing.T) {
	c, err := transporthttp.NewClient()
	require.NoError(t, err)

	expectedErr := errors.New("read error")
	c.Client.Transport = &bodyErrorTransport{err: expectedErr}

	req := transporthttp.Request{
		Method: http.MethodGet,
		URL:    "http://example.com",
	}
	var body testData
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read response body")
}

func Test_Run(t *testing.T) {
	server, c := CreateServer(t, http.StatusOK, func(_ *testing.T, url string, body string) []byte {
		t.Helper()

		val := fmt.Sprintf(`{"val":"data_256", "url": "%s"}`, url)
		if url == "/body/replace" {
			val = fmt.Sprintf(`{"val":"%s", "url": "%s"}`, body, url)
		}
		return []byte(val)
	})
	defer server.Close()
	c.Client = server.Client()

	assert.NotEmpty(t, c)

	req := transporthttp.Request{
		Method: http.MethodPost,
		URL:    server.URL + "/test/url",
		Body:   bytes.NewBuffer([]byte(`{"val": "data_123"}`)),
		Headers: transporthttp.RequestHeaders{
			"Content-Type": "application/json",
		},
	}
	var body testData
	err := c.DoWithRequestAndParse(t.Context(), req, &body)
	require.NoError(t, err)
	assert.Equal(t, "data_256", body.Val)
	assert.Equal(t, "/test/url", body.URL)

	req.ExpectStatusCode = http.StatusOK
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.NoError(t, err)
	assert.Equal(t, "data_256", body.Val)
	assert.Equal(t, "/test/url", body.URL)

	req.ExpectStatusCode = http.StatusBadRequest
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.ErrorIs(t, err, transporthttp.ErrReadResponse)
	assert.Contains(t, err.Error(), "can't read response, reason")
	assert.NotContains(t, err.Error(), "data_256")

	req.ExpectStatusCode = http.StatusBadRequest
	req.ReadResponseAlways = true
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.ErrorIs(t, err, transporthttp.ErrReadResponse)
	assert.Contains(t, err.Error(), "can't read response, reason")
	assert.Contains(t, err.Error(), "data_256")

	req.ExpectStatusCode = http.StatusOK
	req.URL = server.URL + "/body/replace"
	req.Body = bytes.NewBuffer([]byte(`{"val": "data_123"`))
	err = c.DoWithRequestAndParse(t.Context(), req, &body)
	require.ErrorIs(t, err, transporthttp.ErrReadResponse)
	assert.Contains(t, err.Error(), "json Unmarshal")
}

func CreateServer(
	t *testing.T,
	status int,
	fn func(t *testing.T, url string, body string) []byte,
) (*httptest.Server, *transporthttp.Client) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		url := req.URL.String()
		body, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		data := fn(t, url, string(body))
		rw.WriteHeader(status)
		_, err = rw.Write(data)
		assert.NoError(t, err)
	}))
	c, err := transporthttp.NewClient(
		transporthttp.WithTimeout(time.Second),
		transporthttp.WithUnmarshal(json.Unmarshal),
		transporthttp.WithClient(nil),
	)
	require.NoError(t, err)

	return server, c
}

type errorTransport struct {
	err error
}

func (t *errorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, t.err
}

type errorReader struct {
	err error
}

func (e *errorReader) Read(_ []byte) (n int, err error) {
	return 0, e.err
}
func (e *errorReader) Close() error { return nil }

type bodyErrorTransport struct {
	err error
}

func (t *bodyErrorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &errorReader{err: t.err},
	}, nil
}
