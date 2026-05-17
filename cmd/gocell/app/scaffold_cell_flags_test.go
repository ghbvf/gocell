package app

// Unit coverage for validateScaffoldCellFlags (#544 new code). The flag
// table-driven cases drive the required-field, free-text control-char, and
// no-dash rejection branches that the higher-level scaffoldCell integration
// tests do not exercise individually (they stop at the first guard).
//
// The pathsafe.ResolveRoot error wrap in scaffoldCell is intentionally not
// covered: readModule(root) runs before ResolveRoot on the same root, so a
// root that fails EvalSymlinks would already have failed the earlier go.mod
// read. The branch is an unreachable-via-CLI defensive wrap with no logic.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestValidateScaffoldCellFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		team       string
		role       string
		wantErr    bool
		wantSubstr string // substring expected in err.Error() when wantErr
	}{
		{
			name: "valid no-dash id",
			id:   "billingcell", team: "squad", role: "cell-owner",
			wantErr: false,
		},
		{
			name: "missing id",
			id:   "", team: "squad", role: "cell-owner",
			wantErr: true, wantSubstr: "--id is required",
		},
		{
			name: "missing team",
			id:   "billingcell", team: "", role: "cell-owner",
			wantErr: true, wantSubstr: "--team is required",
		},
		{
			name: "missing role",
			id:   "billingcell", team: "squad", role: "",
			wantErr: true, wantSubstr: "--role is required",
		},
		{
			name: "id with control char rejected by validateScaffoldID",
			id:   "bill\ncell", team: "squad", role: "cell-owner",
			wantErr: true, wantSubstr: "control characters",
		},
		{
			// 341-343: free-text --team with a newline must be rejected by
			// validateScaffoldText (YAML-injection guard).
			name: "team with newline rejected by validateScaffoldText",
			id:   "billingcell", team: "squad\nowner: attacker", role: "cell-owner",
			wantErr: true, wantSubstr: "control characters",
		},
		{
			// 355-358: kebab-case cell IDs are rejected (no-dash identifier
			// convention, aligned with scaffoldSlice).
			name: "kebab-case id rejected",
			id:   "billing-cell", team: "squad", role: "cell-owner",
			wantErr: true, wantSubstr: "must not contain '-'",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateScaffoldCellFlags(tc.id, tc.team, tc.role)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateScaffoldCellFlags(%q,%q,%q): want error, got nil",
						tc.id, tc.team, tc.role)
				}
				// errcode.Error.Error() prefers InternalMessage (which carries
				// field=/id= debug context, not the human message), so match
				// against the const-literal Message for structured errors and
				// fall back to Error() for plain fmt.Errorf required-field errors.
				msg := err.Error()
				var ec *errcode.Error
				if errors.As(err, &ec) {
					msg = ec.Message
				}
				if !strings.Contains(msg, tc.wantSubstr) {
					t.Errorf("err message = %q, want substring %q", msg, tc.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateScaffoldCellFlags(%q,%q,%q): unexpected error: %v",
					tc.id, tc.team, tc.role, err)
			}
		})
	}
}

// The kebab rejection must be a structured errcode.Error carrying
// ErrScaffoldInvalidOpts so the CLI surfaces a stable code, not a bare
// fmt.Errorf string.
func TestValidateScaffoldCellFlags_KebabIsStructured(t *testing.T) {
	t.Parallel()
	err := validateScaffoldCellFlags("billing-cell", "squad", "cell-owner")
	if err == nil {
		t.Fatal("kebab id: want error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("kebab id error must be *errcode.Error, got %T (%v)", err, err)
	}
	if ec.Code != ErrScaffoldInvalidOpts {
		t.Errorf("kebab id error code = %q, want %q", ec.Code, ErrScaffoldInvalidOpts)
	}
}
