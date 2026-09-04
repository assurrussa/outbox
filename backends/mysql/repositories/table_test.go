package repositories_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql/repositories"
)

func TestMySQLTableNameValidation(t *testing.T) {
	t.Parallel()

	_, err := repositories.ValidateAndQuoteTableName("")
	require.Error(t, err)

	_, err = repositories.ValidateAndQuoteTableName("   ")
	require.Error(t, err)

	_, err = repositories.ValidateAndQuoteTableName("invalid;drop table")
	require.Error(t, err)

	_, err = repositories.ValidateAndQuoteTableName("123invalid")
	require.Error(t, err)

	_, err = repositories.ValidateAndQuoteTableName("a.b.c")
	require.Error(t, err)

	_, err = repositories.ValidateAndQuoteTableName(strings.Repeat("a", 65))
	require.Error(t, err)

	quoted, err := repositories.ValidateAndQuoteTableName("valid_table_name")
	require.NoError(t, err)
	require.Equal(t, "`valid_table_name`", quoted)

	quoted, err = repositories.ValidateAndQuoteTableName("jobs")
	require.NoError(t, err)
	require.Equal(t, "`jobs`", quoted)

	// Reserved words are valid because identifiers are quoted
	quoted, err = repositories.ValidateAndQuoteTableName("select")
	require.NoError(t, err)
	require.Equal(t, "`select`", quoted)

	quoted, err = repositories.ValidateAndQuoteTableName("order")
	require.NoError(t, err)
	require.Equal(t, "`order`", quoted)

	// Schema/database-qualified identifiers
	quoted, err = repositories.ValidateAndQuoteTableName("mydb.jobs")
	require.NoError(t, err)
	require.Equal(t, "`mydb`.`jobs`", quoted)

	quoted, err = repositories.ValidateAndQuoteTableName("mydb.select")
	require.NoError(t, err)
	require.Equal(t, "`mydb`.`select`", quoted)

	// Already backtick-quoted identifiers
	quoted, err = repositories.ValidateAndQuoteTableName("`mydb`.`jobs`")
	require.NoError(t, err)
	require.Equal(t, "`mydb`.`jobs`", quoted)
}
