package transport_test

import (
	"bytes"
	"context"
	"io"
	nethttp "net/http"
	"testing"

	"github.com/avast/retry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/infrastructure/transport"
	transporthttp "github.com/assurrussa/outbox/infrastructure/transport/http"
	transportmocks "github.com/assurrussa/outbox/infrastructure/transport/mocks"
)

func TestRetryClient_DoWithRequestAndParse_Success(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	var data struct {
		Message string `json:"message"`
	}
	httpClientMock.EXPECT().DoWithRequestAndParse(ctx, request, &data).Return(assert.AnError).Times(1)
	httpClientMock.EXPECT().DoWithRequestAndParse(ctx, request, &data).Return(nil).Times(1)

	err := client.DoWithRequestAndParse(ctx, request, &data)
	require.NoError(t, err)
}

func TestRetryClient_DoWithRequestAndParse_Error(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	var data struct {
		Message string `json:"message"`
	}
	httpClientMock.EXPECT().DoWithRequestAndParse(ctx, request, &data).Return(assert.AnError).Times(1)
	httpClientMock.EXPECT().DoWithRequestAndParse(ctx, request, &data).Return(assert.AnError).Times(1)
	httpClientMock.EXPECT().DoWithRequestAndParse(ctx, request, &data).Return(nil).Times(0)

	err := client.DoWithRequestAndParse(ctx, request, &data)
	require.Error(t, err)
}

func TestRetryClient_DoWithRequest_Success(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().DoWithRequest(ctx, request).Return(createResponse(), nil).Times(1)

	//nolint:bodyclose // tests
	resp, err := client.DoWithRequest(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestRetryClient_DoWithRequest_Error(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().DoWithRequest(ctx, request).Return(createResponse(), assert.AnError).Times(1)
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().DoWithRequest(ctx, request).Return(createResponse(), assert.AnError).Times(1)
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().DoWithRequest(ctx, request).Return(createResponse(), nil).Times(0)

	//nolint:bodyclose // tests
	resp, err := client.DoWithRequest(ctx, request)
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestRetryClient_Do_Success(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().Do(ctx, request.Method, request.URL, request.Body, request.Headers).
		Return(createResponse(), nil).Times(1)

	//nolint:bodyclose // tests
	resp, err := client.Do(ctx, request.Method, request.URL, request.Body, request.Headers)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestRetryClient_Do_Error(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	httpClientMock := transportmocks.NewMockHTTPClient(ctrl)
	client := transport.NewRetryClient(httpClientMock, retry.Attempts(2))

	request := createRequest()
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().Do(ctx, request.Method, request.URL, request.Body, request.Headers).
		Return(createResponse(), assert.AnError).Times(1)
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().Do(ctx, request.Method, request.URL, request.Body, request.Headers).
		Return(createResponse(), assert.AnError).Times(1)
	//nolint:bodyclose // tests
	httpClientMock.EXPECT().Do(ctx, request.Method, request.URL, request.Body, request.Headers).
		Return(createResponse(), nil).Times(0)

	//nolint:bodyclose // tests
	resp, err := client.Do(ctx, request.Method, request.URL, request.Body, request.Headers)
	require.Error(t, err)
	require.Nil(t, resp)
}

func createRequest() transporthttp.Request {
	return transporthttp.Request{
		ExpectStatusCode: nethttp.StatusOK,
		Method:           "POST",
		URL:              "https://example.com/blog/comments",
		Body:             bytes.NewBufferString(`{"message": "lol kek"}`),
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}
}

func createResponse() *nethttp.Response {
	bodyResp := bytes.NewBuffer([]byte(`{"message": "text expected test"}`))

	return &nethttp.Response{
		StatusCode:    nethttp.StatusOK,
		Body:          io.NopCloser(bodyResp),
		ContentLength: int64(bodyResp.Len()),
	}
}
