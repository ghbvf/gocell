package outbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CloneMetadata
// ---------------------------------------------------------------------------

func TestCloneMetadata_NilReturnsEmptyMap(t *testing.T) {
	got := CloneMetadata(nil)
	require.NotNil(t, got, "nil input must return a fresh non-nil map so callers can write unconditionally")
	assert.Empty(t, got)
}

func TestCloneMetadata_EmptyMap(t *testing.T) {
	got := CloneMetadata(map[string]string{})
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestCloneMetadata_DeepCopy(t *testing.T) {
	src := map[string]string{
		"business-key": "val-abc",
		"custom":       "value",
	}
	got := CloneMetadata(src)
	assert.Equal(t, src, got)

	got["business-key"] = "mutated"
	got["new-key"] = "new-value"
	assert.Equal(t, "val-abc", src["business-key"], "source must be isolated from clone mutations")
	_, ok := src["new-key"]
	assert.False(t, ok, "source must not gain keys added to clone")
}

func TestCloneMetadata_MutatingSourceDoesNotAffectClone(t *testing.T) {
	src := map[string]string{"k": "v"}
	got := CloneMetadata(src)
	src["k"] = "mutated"
	src["added"] = "v2"
	assert.Equal(t, "v", got["k"], "clone must be isolated from source mutations")
	_, ok := got["added"]
	assert.False(t, ok)
}
