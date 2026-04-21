package outbox

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/stretchr/testify/assert"
)

func TestNewEntryID_HasPrefix(t *testing.T) {
	id := NewEntryID()
	if !strings.HasPrefix(id, EntryIDPrefix) {
		t.Fatalf("expected prefix %q, got %q", EntryIDPrefix, id)
	}
}

func TestNewEntryID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1024)
	for i := range 1024 {
		id := NewEntryID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate entry ID %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestNewEntryID_PassesSafeIDConstraints(t *testing.T) {
	for range 100 {
		id := NewEntryID()
		assert.True(t, idutil.IsSafeID(id), "entry ID %q must pass IsSafeID", id)
		assert.LessOrEqual(t, len(id), idutil.MaxMetadataIDLen)
	}
}
