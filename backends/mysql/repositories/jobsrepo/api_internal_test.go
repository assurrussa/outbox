package jobsrepo

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coreoutbox "github.com/assurrussa/outbox/outbox"
)

func TestBuildBatchCandidateQueryUsesCapabilityIndexAndDeduplicates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 28, 10, 30, 0, 0, time.UTC)
	capabilities := []coreoutbox.JobCapability{
		{Name: "publish", SchemaVersion: 1},
		{Name: "publish", SchemaVersion: 1},
		{Name: "deliver", SchemaVersion: 2},
	}

	query, args := buildBatchCandidateQuery("jobs", now, capabilities, 3)

	require.Equal(t, 2, strings.Count(query, "FORCE INDEX (jobs_capability_claim_index)"))
	require.Equal(t, 1, strings.Count(query, "UNION ALL"))
	require.NotContains(t, query, "jobs_batch_claim_index")
	require.Contains(t, query, "ORDER BY available_at, created_at, id")
	require.Equal(t, []any{
		"publish", coreoutbox.SchemaVersion(1), now, now, 3,
		"deliver", coreoutbox.SchemaVersion(2), now, now, 3,
		3,
	}, args)
}
