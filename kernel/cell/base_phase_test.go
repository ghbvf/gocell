package cell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestBaseCell_Lifecycle covers the lifecycle funnel in NewBaseCell:
// a declared lifecycle value is parsed to the typed cellvocab.CellLifecycle;
// an empty value defaults to CellLifecycleExperimental (least-mature, mirrors
// NewBaseContract defaulting to active); an unrecognized value is a construction
// error.
func TestBaseCell_Lifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lifecycle string
		want      cellvocab.CellLifecycle
		wantErr   bool
	}{
		{"explicit asset", "asset", cellvocab.CellLifecycleAsset, false},
		{"explicit experimental", "experimental", cellvocab.CellLifecycleExperimental, false},
		{"explicit retired", "retired", cellvocab.CellLifecycleRetired, false},
		{"explicit maintenance", "maintenance", cellvocab.CellLifecycleMaintenance, false},
		{"explicit candidate", "candidate", cellvocab.CellLifecycleCandidate, false},
		{"empty defaults to experimental", "", cellvocab.CellLifecycleExperimental, false},
		{"invalid value errors", "bogus", "", true},
		{"contract lifecycle value rejected", "active", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewBaseCell(&metadata.CellMeta{ID: "c", Lifecycle: tt.lifecycle})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.Lifecycle())
		})
	}
}
