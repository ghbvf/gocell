package cell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestBaseCell_Phase covers the lifecycle-phase funnel in NewBaseCell:
// a declared phase is parsed to the typed cellvocab.Phase; an empty phase
// defaults to PhaseExperimental (least-mature, mirrors NewBaseContract
// defaulting to active); an unrecognized value is a construction error.
func TestBaseCell_Phase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lifecycle string
		want      cellvocab.Phase
		wantErr   bool
	}{
		{"explicit asset", "asset", cellvocab.PhaseAsset, false},
		{"explicit experimental", "experimental", cellvocab.PhaseExperimental, false},
		{"explicit retired", "retired", cellvocab.PhaseRetired, false},
		{"empty defaults to experimental", "", cellvocab.PhaseExperimental, false},
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
			assert.Equal(t, tt.want, c.Phase())
		})
	}
}
