package simpleid_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/jobs/payload/simpleid"
	"github.com/assurrussa/outbox/shared/types"
)

func TestMarshalUnmarshal(t *testing.T) {
	msgID := types.NewMessageID()
	v, err := simpleid.Marshal(msgID)
	require.NoError(t, err)

	msgID2, err := simpleid.Unmarshal[types.MessageID](v)
	require.NoError(t, err)
	assert.Equal(t, msgID, msgID2)
}

func TestMarshal_Error(t *testing.T) {
	_, err := simpleid.Marshal(types.MessageIDNil)
	require.Error(t, err)
}

func TestMustMarshal(t *testing.T) {
	assert.Panics(t, func() {
		simpleid.MustMarshal(types.MessageIDNil)
	})
	assert.NotEmpty(t, simpleid.MustMarshal(types.NewMessageID()))
}
