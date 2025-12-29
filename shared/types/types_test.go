package types_test

import (
	"database/sql/driver"
	"encoding"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/shared/types"
)

var _ interface {
	encoding.TextMarshaler
	encoding.TextUnmarshaler
	gomock.Matcher
} = (*types.JobID)(nil)

func TestParse(t *testing.T) {
	_, err := types.Parse[types.JobID]("abra-cadabra")
	require.Error(t, err)

	JobID, err := types.Parse[types.JobID]("f0317e88-bbfe-11ed-8728-461e464ebed8")
	require.NoError(t, err)
	assert.Equal(t, "f0317e88-bbfe-11ed-8728-461e464ebed8", JobID.String())
}

func TestMustParse(t *testing.T) {
	assert.Panics(t, func() {
		types.MustParse[types.JobID]("abra-cadabra")
	})

	assert.NotPanics(t, func() {
		JobID := types.MustParse[types.JobID]("f0317e88-bbfe-11ed-8728-461e464ebed8")
		assert.Equal(t, "f0317e88-bbfe-11ed-8728-461e464ebed8", JobID.String())
	})
}

func TestJobIDNil(t *testing.T) {
	t.Log(types.JobIDNil)
	assert.Equal(t, types.JobIDNil.String(), uuid.Nil.String())
}

func TestJobID_String(t *testing.T) {
	id := types.NewJobID()
	require.NotEmpty(t, id.String())
	assert.Equal(t, uuid.MustParse(id.String()).String(), id.String())
}

func TestJobID_Scan(t *testing.T) {
	const src = "5c9de646-529c-11ed-81ba-461e464ebed9"

	t.Run("from string and bytes", func(t *testing.T) {
		var id1, id2 types.JobID
		{
			err := id1.Scan(src)
			require.NoError(t, err)
		}
		{
			err := id2.Scan([]byte(src))
			require.NoError(t, err)
		}
		assert.Equal(t, id1.String(), id2.String())
		assert.Equal(t, getValueAsString(t, id1), getValueAsString(t, id2))
	})

	t.Run("from NULL", func(t *testing.T) {
		for _, src := range []any{nil, []byte(nil), []byte{}, ""} {
			t.Run("", func(t *testing.T) {
				var id types.JobID
				err := id.Scan(src)
				require.NoError(t, err)
				assert.Equal(t, types.JobIDNil.String(), id.String())
				assert.Equal(t, types.JobIDNil.String(), getValueAsString(t, id))
			})
		}
	})
}

func TestJobID_MarshalText(t *testing.T) {
	JobID := types.MustParse[types.JobID]("f0317e88-bbfe-11ed-8728-461e464ebed8")
	v, err := JobID.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "f0317e88-bbfe-11ed-8728-461e464ebed8", string(v))

	var JobID2 types.JobID
	err = JobID2.UnmarshalText(v)
	require.NoError(t, err)
	assert.Equal(t, JobID.String(), JobID2.String())
}

func TestJobID_IsZero(t *testing.T) {
	id := types.NewJobID()
	assert.False(t, id.IsZero())
	assert.True(t, types.JobIDNil.IsZero())
	assert.Equal(t, uuid.Nil.String(), types.JobIDNil.String())
}

func TestJobID_Matches(t *testing.T) {
	id := types.NewJobID()
	id2 := types.MustParse[types.JobID](id.String())
	assert.Equal(t, id, id2)
	assert.True(t, id.Matches(id2))
	assert.NotEqual(t, id, id2.String())
	assert.NotEqual(t, id, types.NewJobID())
}

func TestJobID_Validate(t *testing.T) {
	require.NoError(t, types.NewJobID().Validate())
	require.Error(t, types.JobID{}.Validate())
	require.Error(t, types.JobIDNil.Validate())
}

func getValueAsString(t *testing.T, valuer driver.Valuer) string {
	t.Helper()

	v, err := valuer.Value()
	require.NoError(t, err)
	vv, ok := v.(string)
	require.True(t, ok)
	return vv
}
