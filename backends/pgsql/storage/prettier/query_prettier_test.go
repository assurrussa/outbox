package prettier

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrettyPlaceholderDoubleDigits(t *testing.T) {
	query := "INSERT INTO t (c1, c2, c3, c4, c5, c6, c7, c8, c9, c10, c11) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)"
	args := []any{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10", "v11"}

	result := pretty(query, PlaceholderDollar, args...)
	require.NotContains(t, result, `"v1"0`)
	require.NotContains(t, result, `"v1"1`)
	require.Contains(t, result, `"v1"`)
	require.Contains(t, result, `"v10"`)
	require.Contains(t, result, `"v11"`)
}
