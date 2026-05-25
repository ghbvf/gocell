package outbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/idutil"
)

var errEntropyFailedForTest = errors.New("outbox_test: entropy stub failure")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errEntropyFailedForTest }

func TestNewEntryID_RandFailure(t *testing.T) {
	id, err := newEntryID(failingReader{})
	require.Error(t, err)
	assert.Empty(t, id)
	assert.ErrorIs(t, err, errEntropyFailedForTest)
	assert.ErrorContains(t, err, "outbox: read entropy")
}

func TestNewEntryID_HasPrefix(t *testing.T) {
	id := MustNewEntryID()
	if !strings.HasPrefix(id, EntryIDPrefix) {
		t.Fatalf("expected prefix %q, got %q", EntryIDPrefix, id)
	}
}

func TestNewEntryID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1024)
	for i := range 1024 {
		id := MustNewEntryID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate entry ID %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestNewEntryID_PassesSafeIDConstraints(t *testing.T) {
	for range 100 {
		id := MustNewEntryID()
		assert.True(t, idutil.IsSafeID(id), "entry ID %q must pass IsSafeID", id)
		assert.LessOrEqual(t, len(id), idutil.MaxMetadataIDLen)
	}
}
