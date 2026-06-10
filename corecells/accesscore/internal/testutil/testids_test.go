package testutil

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTestID_CanonicalLowercaseUUID asserts the helper returns a canonical
// lowercase UUID string accepted by httputil.ParseUUIDPathParam (which is the
// reason this helper exists in the first place).
func TestTestID_CanonicalLowercaseUUID(t *testing.T) {
	id := TestID("any-label")
	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	assert.Equal(t, parsed.String(), id, "TestID must return canonical form")
}

// TestTestID_DeterministicSameLabel asserts the same label always yields the
// same UUID across calls (callers rely on this for fixture stability).
func TestTestID_DeterministicSameLabel(t *testing.T) {
	assert.Equal(t, TestID("user-1"), TestID("user-1"))
}

// TestTestID_DistinctLabelsDistinctUUIDs catches regressions where the SHA-1
// namespace derivation collapses (e.g. wrong namespace, empty input).
func TestTestID_DistinctLabelsDistinctUUIDs(t *testing.T) {
	assert.NotEqual(t, TestID("user-1"), TestID("user-2"))
	assert.NotEqual(t, TestID(""), TestID("any-label"))
}
