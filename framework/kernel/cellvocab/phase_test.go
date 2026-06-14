package cellvocab

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestParseCellLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    CellLifecycle
		wantErr bool
	}{
		{"experimental", CellLifecycleExperimental, false},
		{"candidate", CellLifecycleCandidate, false},
		{"asset", CellLifecycleAsset, false},
		{"maintenance", CellLifecycleMaintenance, false},
		{"retired", CellLifecycleRetired, false},
		{"", "", true},
		{"Experimental", "", true},
		{"stable", "", true},
		{"draft", "", true}, // contract Lifecycle value must not leak into CellLifecycle
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCellLifecycle(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr))
				assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCellLifecycles_Ordered locks the canonical maturity ordering. CellLifecycleRank and the
// governance slice≤cell check depend on this exact ascending order.
func TestCellLifecycles_Ordered(t *testing.T) {
	t.Parallel()
	want := [5]CellLifecycle{
		CellLifecycleExperimental, CellLifecycleCandidate, CellLifecycleAsset, CellLifecycleMaintenance, CellLifecycleRetired,
	}
	assert.Equal(t, want, AllCellLifecycles())
}

func TestCellLifecycleRank(t *testing.T) {
	t.Parallel()
	// Ascending: experimental < candidate < asset < maintenance < retired.
	assert.Equal(t, 0, CellLifecycleRank("experimental"))
	assert.Equal(t, 1, CellLifecycleRank("candidate"))
	assert.Equal(t, 2, CellLifecycleRank("asset"))
	assert.Equal(t, 3, CellLifecycleRank("maintenance"))
	assert.Equal(t, 4, CellLifecycleRank("retired"))
	assert.Equal(t, -1, CellLifecycleRank("stable"))
	assert.Equal(t, -1, CellLifecycleRank(""))

	// Method form mirrors the string-keyed lookup.
	assert.Equal(t, 2, CellLifecycleAsset.Rank())
	assert.True(t, CellLifecycleExperimental.Rank() < CellLifecycleAsset.Rank())
	assert.True(t, CellLifecycleAsset.Rank() < CellLifecycleRetired.Rank())
}

// TestCellLifecycle_AllConstsInAllCellLifecycles is the test-side mirror of the
// CELL-LIFECYCLE-RANK-COMPLETENESS-01 archtest: every declared CellLifecycle const must
// appear in the ordered AllCellLifecycles array (otherwise CellLifecycleRank returns -1
// and the governance ordering silently breaks).
func TestCellLifecycle_AllConstsInAllCellLifecycles(t *testing.T) {
	t.Parallel()
	for _, p := range []CellLifecycle{
		CellLifecycleExperimental, CellLifecycleCandidate, CellLifecycleAsset, CellLifecycleMaintenance, CellLifecycleRetired,
	} {
		assert.GreaterOrEqual(t, CellLifecycleRank(string(p)), 0,
			"CellLifecycle const %q must appear in the AllCellLifecycles array", p)
	}
}
