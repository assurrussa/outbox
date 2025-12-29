package strings_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/assurrussa/outbox/shared/strings"
)

func TestSelectFirst(t *testing.T) {
	assert.Empty(t, strings.SelectFirst(""))
	assert.Equal(t, " ", strings.SelectFirst(" "))
	assert.Equal(t, "table_test_name", strings.SelectFirst("table_test_name"))
	assert.Equal(t, "table_test_name_2", strings.SelectFirst("table_test_name", "table_test_name_2"))
}
