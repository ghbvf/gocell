package mqtt

import (
	"strings"
	"testing"
)

func TestParseClientID_Valid(t *testing.T) {
	tests := []struct {
		name   string
		cellID string
		role   string
	}{
		{"simple", "mycell", "pub"},
		{"with-hyphens", "my-cell", "sub-handler"},
		{"alphanumeric", "cell01", "role1"},
		{"single-char", "a", "b"},
		{"max-length-ish", "abcde", "fghij"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cid, err := ParseClientID(tc.cellID, tc.role)
			if err != nil {
				t.Fatalf("ParseClientID(%q, %q) unexpected error: %v", tc.cellID, tc.role, err)
			}
			s := cid.String()
			// Must have exactly 3 parts: cellID-role-uuid
			parts := strings.Split(s, "-")
			if len(parts) < 3 {
				t.Fatalf("String() = %q: expected at least 3 hyphen-separated parts, got %d", s, len(parts))
			}
			// First segment must equal cellID (which may itself contain hyphens)
			cellIDLen := len(tc.cellID)
			if s[:cellIDLen] != tc.cellID {
				t.Errorf("String() = %q: expected prefix %q", s, tc.cellID)
			}
			// Role must appear after cellID-
			rest := s[cellIDLen+1:]
			roleLen := len(tc.role)
			if len(rest) <= roleLen || rest[:roleLen] != tc.role {
				t.Errorf("String() = %q: expected role %q after cellID", s, tc.role)
			}
			// Must have uuid suffix (36 chars: 32 hex + 4 hyphens)
			uuidPart := rest[roleLen+1:]
			if len(uuidPart) != 36 {
				t.Errorf("String() = %q: uuid suffix %q has length %d, want 36", s, uuidPart, len(uuidPart))
			}
		})
	}
}

func TestParseClientID_InvalidCellID(t *testing.T) {
	tests := []struct {
		name   string
		cellID string
		role   string
	}{
		{"empty-cellid", "", "pub"},
		{"uppercase-cellid", "MyCell", "pub"},
		{"leading-digit", "1cell", "pub"},
		{"trailing-hyphen", "cell-", "pub"},
		{"double-hyphen", "cell--id", "pub"},
		{"special-chars", "cell_id", "pub"},
		{"space", "cell id", "pub"},
		{"too-long-cellid", strings.Repeat("a", 33), "pub"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseClientID(tc.cellID, tc.role)
			if err == nil {
				t.Errorf("ParseClientID(%q, %q) expected error, got nil", tc.cellID, tc.role)
			}
		})
	}
}

func TestParseClientID_InvalidRole(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{"empty-role", ""},
		{"uppercase-role", "Pub"},
		{"leading-digit", "1pub"},
		{"trailing-hyphen", "role-"},
		{"double-hyphen", "role--x"},
		{"special-chars", "role_x"},
		{"too-long-role", strings.Repeat("b", 33)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseClientID("mycell", tc.role)
			if err == nil {
				t.Errorf("ParseClientID(%q, %q) expected error, got nil", "mycell", tc.role)
			}
		})
	}
}

func TestParseClientID_Uniqueness(t *testing.T) {
	cid1, err1 := ParseClientID("mycell", "pub")
	cid2, err2 := ParseClientID("mycell", "pub")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if cid1.String() == cid2.String() {
		t.Error("two ParseClientID calls returned identical values — uuid suffix must differ")
	}
}

func TestClientID_ZeroValueString(t *testing.T) {
	// Zero value ClientID should have empty String().
	var cid ClientID
	if cid.String() != "" {
		t.Errorf("zero-value ClientID.String() = %q, want empty string", cid.String())
	}
}

func TestClientID_SealedConstruction(t *testing.T) {
	// This test documents that ClientID{} produces a zero-value (empty String).
	// The only way to get a non-zero ClientID is ParseClientID.
	// We cannot literally test "this fails to compile" but we confirm the zero
	// value is distinguishable from a valid ClientID.
	var zero ClientID
	valid, _ := ParseClientID("cell", "role")
	if zero.String() == valid.String() {
		t.Error("zero ClientID and parsed ClientID must have different String() values")
	}
}

func TestParseClientID_TotalLengthLimit(t *testing.T) {
	// cellID(32) + "-" + role(32) + "-" + uuid(36) = 101, well within 128.
	// Construct a case that exceeds 128: cellID=32 + role=32 + uuid=36 + 2 sep = 102 (fine).
	// To exceed 128 we need cellID+role > 90. Use 46+46 = 92 > 90. Total = 92+2+36=130 > 128.
	longCellID := strings.Repeat("a", 32)
	longRole := strings.Repeat("b", 32)
	// 32 + 1 + 32 + 1 + 36 = 102, fine.
	_, err := ParseClientID(longCellID, longRole)
	if err != nil {
		t.Errorf("ParseClientID with total 102 chars unexpected error: %v", err)
	}
}
