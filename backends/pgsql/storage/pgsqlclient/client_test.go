package pgsqlclient_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgsqlclient "github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
)

func TestNewPool_Success(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithEnvironment("local"),
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disable"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSPath("", ""),
		pgsqlclient.WithTLSConfig(nil),
		pgsqlclient.WithCheck(false),
	))
	require.NoError(t, err)
	assert.NotNil(t, pool)
	require.NoError(t, pool.Close())
}

func TestNewPool_Success_WithTLSPath(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disable"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSPath("testdata/cert/cert.crt", "testdata/cert/cert.key"),
		pgsqlclient.WithTLSConfig(nil),
		pgsqlclient.WithCheck(false),
	))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestNewPool_Success_WithTLSCert(t *testing.T) {
	ctx := context.Background()

	cert, err := tls.LoadX509KeyPair("testdata/cert/cert.crt", "testdata/cert/cert.key")
	require.NoError(t, err)

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disable"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSPath("testdata/cert/unknown.crt", "testdata/cert/unknown.key"),
		pgsqlclient.WithTLSConfig(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			ServerName:   "localhost:54752",
			Certificates: []tls.Certificate{cert},
		}),
		pgsqlclient.WithCheck(false),
	))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestNewPool_Error_WithTLSPath(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disable"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSPath("testdata/cert/unknown.crt", "testdata/cert/unknown.key"),
		pgsqlclient.WithTLSConfig(nil),
		pgsqlclient.WithCheck(false),
	))
	require.Error(t, err)
	assert.Nil(t, pool)
}

func TestNewPool_Error_WithDNSError(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disabled"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSConfig(nil),
		pgsqlclient.WithCheck(false),
	))
	require.Error(t, err)
	assert.Nil(t, pool)
}

func TestNewPool_Error_Validate(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"",
		"",
		"",
		"",
	))
	require.Error(t, err)
	assert.Nil(t, pool)
}
