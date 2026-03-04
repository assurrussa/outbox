package deployenv

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAppConnFromEnv_DSNPriority(t *testing.T) {
	t.Parallel()

	t.Run("OUTBOX_PICODATA_DSN has top priority", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataDSN:     "postgres://admin:secret@localhost:5049?sslmode=disable",
			envTestOutboxPicodataDSN: "postgres://admin:other@127.0.0.1:5050?sslmode=require",
		}))
		require.NoError(t, err)
		require.Equal(t, "postgres://admin:secret@127.0.0.1:5049?sslmode=disable", cfg.ConnectionURL())
		require.Equal(t, "127.0.0.1", cfg.Host)
		require.Equal(t, 5049, cfg.Port)
	})

	t.Run("fallback to TEST_OUTBOXLIB_PICODATA_DSN", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envTestOutboxPicodataDSN: "postgres://admin:secret@localhost:5049?sslmode=disable",
		}))
		require.NoError(t, err)
		require.Equal(t, "postgres://admin:secret@127.0.0.1:5049?sslmode=disable", cfg.ConnectionURL())
	})
}

func TestLoadAppConnFromEnv_BuildFromFields(t *testing.T) {
	t.Parallel()

	t.Run("uses defaults", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadAppConnFromEnv(mapLookup(nil))
		require.NoError(t, err)

		require.Equal(t, defaultHost, cfg.Host)
		require.Equal(t, defaultPort, cfg.Port)
		require.Equal(t, defaultUser, cfg.User)
		require.Equal(t, defaultPassword, cfg.Password)
		require.Equal(t, defaultSSLMode, cfg.SSLMode)
		require.Equal(t, "postgres://admin:passWord%21123@127.0.0.1:5049?sslmode=disable", cfg.ConnectionURL())
	})

	t.Run("normalizes localhost", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataHost: "localhost",
			envOutboxPicodataPort: "5100",
		}))
		require.NoError(t, err)

		require.Equal(t, "127.0.0.1", cfg.Host)
		require.Equal(t, 5100, cfg.Port)
		require.Equal(t, "postgres://admin:passWord%21123@127.0.0.1:5100?sslmode=disable", cfg.ConnectionURL())
	})

	t.Run("rejects 0.0.0.0 host", func(t *testing.T) {
		t.Parallel()

		_, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataHost: "0.0.0.0",
		}))
		require.ErrorContains(t, err, "must not be 0.0.0.0")
	})

	t.Run("rejects invalid port", func(t *testing.T) {
		t.Parallel()

		_, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataPort: "abc",
		}))
		require.ErrorContains(t, err, "invalid OUTBOX_PICODATA_PORT")
	})
}

func TestLoadAppConnFromEnv_DSNValidation(t *testing.T) {
	t.Parallel()

	t.Run("normalizes localhost and fills default port", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataDSN: "postgres://admin:secret@localhost?sslmode=require",
		}))
		require.NoError(t, err)
		require.Equal(t, "postgres://admin:secret@127.0.0.1:5049?sslmode=require", cfg.ConnectionURL())
	})

	t.Run("rejects 0.0.0.0 host in dsn", func(t *testing.T) {
		t.Parallel()

		_, err := LoadAppConnFromEnv(mapLookup(map[string]string{
			envOutboxPicodataDSN: "postgres://admin:secret@0.0.0.0:5049?sslmode=disable",
		}))
		require.ErrorContains(t, err, "must not be 0.0.0.0")
	})
}

func TestValidateRuntimeEnv(t *testing.T) {
	t.Parallel()

	t.Run("detects listen conflict", func(t *testing.T) {
		t.Parallel()

		err := ValidateRuntimeEnv(mapLookup(map[string]string{
			envPicodataListen:       "0.0.0.0:3301",
			envPicodataIProtoListen: "0.0.0.0:3301",
		}))
		require.ErrorContains(t, err, "PICODATA_LISTEN and PICODATA_IPROTO_LISTEN")
		require.ErrorContains(t, err, "remove PICODATA_LISTEN")
	})

	t.Run("detects advertise conflict", func(t *testing.T) {
		t.Parallel()

		err := ValidateRuntimeEnv(mapLookup(map[string]string{
			envPicodataPGAdvertise:     "node:5001",
			envPicodataIProtoAdvertise: "node:3301",
		}))
		require.ErrorContains(t, err, "PICODATA_PG_ADVERTISE and PICODATA_IPROTO_ADVERTISE")
		require.ErrorContains(t, err, "remove PICODATA_PG_ADVERTISE")
	})

	t.Run("no conflict", func(t *testing.T) {
		t.Parallel()

		err := ValidateRuntimeEnv(mapLookup(map[string]string{
			envPicodataIProtoListen: "0.0.0.0:3301",
		}))
		require.NoError(t, err)
	})
}

func mapLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
