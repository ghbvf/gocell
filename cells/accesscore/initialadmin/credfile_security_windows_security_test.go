//go:build windows

package initialadmin

import (
	"testing"
)

// TestSddlHasAllowACEForSID_AcceptsAllowACE verifies that sddlHasAllowACEForSID
// returns true for valid ALLOW ACEs with full-access masks in both full S-1-*
// and SDDL alias forms (B1 regression guard).
func TestSddlHasAllowACEForSID_AcceptsAllowACE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sddl   string
		sidStr string
		want   bool
	}{
		{
			name:   "full SID form GA mask matches",
			sddl:   "D:P(A;;GA;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "full SID form FA mask matches",
			sddl:   "D:P(A;;FA;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "SDDL alias SY matches S-1-5-18 with GA",
			sddl:   "D:P(A;;GA;;;SY)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "SDDL alias SY matches S-1-5-18 with FA",
			sddl:   "D:P(A;;FA;;;SY)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "SDDL alias BA matches S-1-5-32-544 with GA",
			sddl:   "D:P(A;;GA;;;BA)",
			sidStr: "S-1-5-32-544",
			want:   true,
		},
		{
			name:   "SDDL alias BA matches S-1-5-32-544 with FA",
			sddl:   "D:P(A;;FA;;;BA)",
			sidStr: "S-1-5-32-544",
			want:   true,
		},
		{
			name:   "full SID form S-1-5-32-544 GA matches",
			sddl:   "D:P(A;;GA;;;S-1-5-32-544)",
			sidStr: "S-1-5-32-544",
			want:   true,
		},
		{
			name:   "multiple ACEs — correct SID is present",
			sddl:   "D:P(A;;GA;;;S-1-5-18)(A;;GA;;;S-1-5-32-544)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "inheritable AI type accepted",
			sddl:   "D:P(AI;;GA;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "different SID does not match",
			sddl:   "D:P(A;;GA;;;SY)",
			sidStr: "S-1-5-32-544",
			want:   false,
		},
		{
			name:   "empty SDDL does not match",
			sddl:   "",
			sidStr: "S-1-5-18",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sddlHasAllowACEForSID(tc.sddl, tc.sidStr)
			if got != tc.want {
				t.Errorf("sddlHasAllowACEForSID(%q, %q) = %v, want %v", tc.sddl, tc.sidStr, got, tc.want)
			}
		})
	}
}

// TestSddlHasAllowACEForSID_RejectsDenyACE verifies that DENY ACEs (type "D")
// are not accepted even when the SID and full-access mask are present (B1).
// A DENY ACE must not be mistaken for granted access.
func TestSddlHasAllowACEForSID_RejectsDenyACE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sddl   string
		sidStr string
	}{
		{
			name:   "DENY ACE with full SID",
			sddl:   "D:P(D;;GA;;;S-1-5-18)",
			sidStr: "S-1-5-18",
		},
		{
			name:   "DENY ACE with alias SY",
			sddl:   "D:P(D;;GA;;;SY)",
			sidStr: "S-1-5-18",
		},
		{
			name:   "DENY ACE with FA mask",
			sddl:   "D:P(D;;FA;;;S-1-5-32-544)",
			sidStr: "S-1-5-32-544",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if sddlHasAllowACEForSID(tc.sddl, tc.sidStr) {
				t.Errorf("sddlHasAllowACEForSID(%q, %q) = true; DENY ACE must not be accepted", tc.sddl, tc.sidStr)
			}
		})
	}
}

// TestSddlHasAllowACEForSID_AcceptsAnyNonEmptyMask verifies that any non-empty
// rights field in an ALLOW ACE is accepted. Windows may emit GENERIC_ALL as
// "GA", "FA", or hex forms like "0x1f01ff" depending on OS version and NTFS
// generic-to-specific mapping. Strict mask validation was removed to prevent
// false-positive tamper reports (V-A12 / Failure 1). The E3 gap (no mask
// narrowing check) is tracked in the V-A12 backlog entry.
func TestSddlHasAllowACEForSID_AcceptsAnyNonEmptyMask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sddl   string
		sidStr string
		want   bool
	}{
		{
			name:   "READ_CONTROL mask (RC) now accepted (E3 gap, V-A12)",
			sddl:   "D:P(A;;RC;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "hex mask 0x1f01ff accepted",
			sddl:   "D:P(A;;0x1f01ff;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   true,
		},
		{
			name:   "empty rights field rejected",
			sddl:   "D:P(A;;;;;S-1-5-18)",
			sidStr: "S-1-5-18",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sddlHasAllowACEForSID(tc.sddl, tc.sidStr)
			if got != tc.want {
				t.Errorf("sddlHasAllowACEForSID(%q, %q) = %v, want %v", tc.sddl, tc.sidStr, got, tc.want)
			}
		})
	}
}
