package uid

import (
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uuidV4Re matches a UUID v4: 8-4-4-4-12 hex with version nibble = 4 and
// variant high bits = 8/9/a/b.
var uuidV4Re = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestNew(t *testing.T) {
	t.Run("format is valid UUID v4", func(t *testing.T) {
		for range 20 {
			id := New()
			assert.Regexp(t, uuidV4Re, id, "generated ID %q is not a valid UUID v4", id)
			assert.Len(t, id, 36, "UUID v4 string must be 36 characters")
		}
	})

	t.Run("uniqueness across 1000 sequential calls", func(t *testing.T) {
		seen := make(map[string]bool, 1000)
		for range 1000 {
			id := New()
			require.False(t, seen[id], "duplicate ID generated: %s", id)
			seen[id] = true
		}
	})

	t.Run("100 concurrent calls produce 100 unique IDs", func(t *testing.T) {
		const n = 100
		ids := make([]string, n)
		var wg sync.WaitGroup
		wg.Add(n)

		for i := range n {
			go func(idx int) {
				defer wg.Done()
				ids[idx] = New()
			}(i)
		}
		wg.Wait()

		seen := make(map[string]bool, n)
		for _, id := range ids {
			assert.Regexp(t, uuidV4Re, id)
			require.False(t, seen[id], "duplicate ID from concurrent generation: %s", id)
			seen[id] = true
		}
		assert.Len(t, seen, n)
	})
}

func TestNewWithPrefix(t *testing.T) {
	t.Run("prepends prefix", func(t *testing.T) {
		id := NewWithPrefix("usr")
		assert.Regexp(t, `^usr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
		assert.Len(t, id, 4+36) // "usr-" + UUID
	})

	t.Run("different prefixes", func(t *testing.T) {
		prefixes := []string{"sess", "cfg", "audit", "evt", "ver"}
		for _, p := range prefixes {
			id := NewWithPrefix(p)
			assert.Contains(t, id, p+"-")
		}
	})
}
