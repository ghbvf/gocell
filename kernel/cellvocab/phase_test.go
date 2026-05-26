package cellvocab

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestParsePhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    Phase
		wantErr bool
	}{
		{"experimental", PhaseExperimental, false},
		{"candidate", PhaseCandidate, false},
		{"asset", PhaseAsset, false},
		{"maintenance", PhaseMaintenance, false},
		{"retired", PhaseRetired, false},
		{"", "", true},
		{"Experimental", "", true},
		{"stable", "", true},
		{"draft", "", true}, // contract Lifecycle value must not leak into Phase
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePhase(tt.in)
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

// TestPhases_Ordered locks the canonical maturity ordering. PhaseRank and the
// governance slice≤cell check depend on this exact ascending order.
func TestPhases_Ordered(t *testing.T) {
	t.Parallel()
	want := []Phase{
		PhaseExperimental, PhaseCandidate, PhaseAsset, PhaseMaintenance, PhaseRetired,
	}
	assert.Equal(t, want, Phases[:])
}

func TestPhaseRank(t *testing.T) {
	t.Parallel()
	// Ascending: experimental < candidate < asset < maintenance < retired.
	assert.Equal(t, 0, PhaseRank("experimental"))
	assert.Equal(t, 1, PhaseRank("candidate"))
	assert.Equal(t, 2, PhaseRank("asset"))
	assert.Equal(t, 3, PhaseRank("maintenance"))
	assert.Equal(t, 4, PhaseRank("retired"))
	assert.Equal(t, -1, PhaseRank("stable"))
	assert.Equal(t, -1, PhaseRank(""))

	// Method form mirrors the string-keyed lookup.
	assert.Equal(t, 2, PhaseAsset.Rank())
	assert.True(t, PhaseExperimental.Rank() < PhaseAsset.Rank())
	assert.True(t, PhaseAsset.Rank() < PhaseRetired.Rank())
}

// TestPhase_AllConstsInPhases is the test-side mirror of the
// CELL-PHASE-RANK-COMPLETENESS-01 archtest: every declared Phase const must
// appear in the ordered Phases slice (otherwise PhaseRank returns -1 and the
// governance ordering silently breaks).
func TestPhase_AllConstsInPhases(t *testing.T) {
	t.Parallel()
	for _, p := range []Phase{
		PhaseExperimental, PhaseCandidate, PhaseAsset, PhaseMaintenance, PhaseRetired,
	} {
		assert.GreaterOrEqual(t, PhaseRank(string(p)), 0,
			"Phase const %q must appear in the ordered Phases slice", p)
	}
}
