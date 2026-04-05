package id

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("format", func(t *testing.T) {
		got := New("usr")
		require.True(t, strings.HasPrefix(got, "usr-"))
		// prefix "usr-" (4) + 16 hex chars = 20
		assert.Len(t, got, 20)
	})

	t.Run("uniqueness", func(t *testing.T) {
		seen := make(map[string]bool, 1000)
		for range 1000 {
			id := New("test")
			assert.False(t, seen[id], "duplicate ID generated: %s", id)
			seen[id] = true
		}
	})

	t.Run("different prefixes", func(t *testing.T) {
		assert.True(t, strings.HasPrefix(New("sess"), "sess-"))
		assert.True(t, strings.HasPrefix(New("cfg"), "cfg-"))
		assert.True(t, strings.HasPrefix(New("audit"), "audit-"))
	})
}
