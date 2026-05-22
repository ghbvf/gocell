package main

import (
	"strings"
	"testing"
)

// stubModule is a test-only CellModule implementation that returns a fixed ID.
type stubModule struct{ id string }

func (s stubModule) ID() string { return s.id }

// TestAssertModuleIDsMatch covers the three outcomes of assertModuleIDsMatch:
// happy path, length mismatch, and ID mismatch.
func TestAssertModuleIDsMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		cellIDs   []string
		mods      []CellModule
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "happy_path_single",
			cellIDs: []string{"ordercell"},
			mods:    []CellModule{stubModule{"ordercell"}},
			wantErr: false,
		},
		{
			name:    "happy_path_multiple",
			cellIDs: []string{"alpha", "beta"},
			mods:    []CellModule{stubModule{"alpha"}, stubModule{"beta"}},
			wantErr: false,
		},
		{
			name:      "length_mismatch_more_cells",
			cellIDs:   []string{"alpha", "beta"},
			mods:      []CellModule{stubModule{"alpha"}},
			wantErr:   true,
			errSubstr: "length mismatch",
		},
		{
			name:      "length_mismatch_more_mods",
			cellIDs:   []string{"alpha"},
			mods:      []CellModule{stubModule{"alpha"}, stubModule{"beta"}},
			wantErr:   true,
			errSubstr: "length mismatch",
		},
		{
			name:      "id_mismatch_first_element",
			cellIDs:   []string{"alpha"},
			mods:      []CellModule{stubModule{"wrong"}},
			wantErr:   true,
			errSubstr: "drift",
		},
		{
			name:      "id_mismatch_second_element",
			cellIDs:   []string{"alpha", "beta"},
			mods:      []CellModule{stubModule{"alpha"}, stubModule{"wrong"}},
			wantErr:   true,
			errSubstr: "drift",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assertModuleIDsMatch("todoorder", tc.cellIDs, tc.mods)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("expected error to contain %q; got: %v", tc.errSubstr, err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}
