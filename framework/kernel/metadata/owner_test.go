package metadata

import (
	"strings"
	"testing"
)

func TestContractOwner_Resolution(t *testing.T) {
	tests := []struct {
		name        string
		ownerCell   string
		wantCell    string
		wantIsCell  bool
		wantIsFrwrk bool
	}{
		{
			name:        "cell owner resolves to cell",
			ownerCell:   "accesscore",
			wantCell:    "accesscore",
			wantIsCell:  true,
			wantIsFrwrk: false,
		},
		{
			name:        "framework sentinel resolves to framework",
			ownerCell:   FrameworkOwnerSentinel,
			wantCell:    "",
			wantIsCell:  false,
			wantIsFrwrk: true,
		},
		{
			name:        "empty owner is a cell-kind owner with empty id (CH-01 error state, not framework)",
			ownerCell:   "",
			wantCell:    "",
			wantIsCell:  true,
			wantIsFrwrk: false,
		},
		{
			name:        "a typo'd cell is still a cell owner (REF-03 still fires, not silently framework)",
			ownerCell:   "accescore",
			wantCell:    "accescore",
			wantIsCell:  true,
			wantIsFrwrk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ContractMeta{}
			c.OwnerCell = tt.ownerCell
			owner := c.Owner()

			if got := owner.IsFramework(); got != tt.wantIsFrwrk {
				t.Errorf("IsFramework() = %v, want %v", got, tt.wantIsFrwrk)
			}
			gotCell, gotOK := owner.Cell()
			if gotOK != tt.wantIsCell {
				t.Errorf("Cell() ok = %v, want %v", gotOK, tt.wantIsCell)
			}
			if gotCell != tt.wantCell {
				t.Errorf("Cell() id = %q, want %q", gotCell, tt.wantCell)
			}
		})
	}
}

// TestContractOwner_FrameworkYieldsNoCell is the type-level Hard guarantee: a
// framework owner has no code path that produces a cell id, so it can never be
// indexed into project.Cells as if it were a cell.
func TestContractOwner_FrameworkYieldsNoCell(t *testing.T) {
	c := &ContractMeta{}
	c.OwnerCell = FrameworkOwnerSentinel
	if _, ok := c.Owner().Cell(); ok {
		t.Fatal("framework owner must not yield a cell id (Cell() ok must be false)")
	}
}

// TestFrameworkOwnerSentinel_NotALegalCellID documents that the sentinel cannot
// collide with a real cell id (cells are no-dash concat ids; the leading
// underscore is reserved).
func TestFrameworkOwnerSentinel_NotALegalCellID(t *testing.T) {
	if FrameworkOwnerSentinel == "" || FrameworkOwnerSentinel[0] != '_' {
		t.Fatalf("FrameworkOwnerSentinel %q must be a reserved underscore-prefixed value", FrameworkOwnerSentinel)
	}
	if strings.Contains(FrameworkOwnerSentinel, "-") {
		t.Fatalf("FrameworkOwnerSentinel %q must not contain a dash (cell IDs are no-dash concat style)", FrameworkOwnerSentinel)
	}
}
