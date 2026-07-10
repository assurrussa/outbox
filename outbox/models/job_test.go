package models_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

func TestJobJSONOmitsLeaseToken(t *testing.T) {
	data, err := json.Marshal(models.Job{
		SchemaVersion: 2,
		LeaseToken:    types.NewLeaseToken(),
	})
	require.NoError(t, err)

	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &payload))
	require.NotContains(t, payload, "leaseToken")
	require.NotContains(t, payload, "deduplicationKey")

	var schemaVersion int32
	require.NoError(t, json.Unmarshal(payload["schemaVersion"], &schemaVersion))
	require.Equal(t, int32(2), schemaVersion)
}
