package mqtt

import (
	"strings"
	"testing"
)

// assertEphemeralClientIDValid checks the structural invariants of a valid
// ParseEphemeralClientID result: prefix matches cellID, role segment follows,
// and the trailing UUID suffix is 36 characters.
func assertEphemeralClientIDValid(t *testing.T, cellID, role string) {
	t.Helper()
	cid, err := ParseEphemeralClientID(cellID, role)
	if err != nil {
		t.Fatalf("ParseEphemeralClientID(%q, %q) unexpected error: %v", cellID, role, err)
	}
	s := cid.String()
	// Must have at least 3 parts: cellID-role-uuid (uuid contains hyphens).
	parts := strings.Split(s, "-")
	if len(parts) < 3 {
		t.Fatalf("String() = %q: expected at least 3 hyphen-separated parts, got %d", s, len(parts))
	}
	// First segment must equal cellID (which may itself contain hyphens).
	cellIDLen := len(cellID)
	if s[:cellIDLen] != cellID {
		t.Errorf("String() = %q: expected prefix %q", s, cellID)
	}
	// Role must appear after cellID-.
	rest := s[cellIDLen+1:]
	roleLen := len(role)
	if len(rest) <= roleLen || rest[:roleLen] != role {
		t.Errorf("String() = %q: expected role %q after cellID", s, role)
	}
	// Must have uuid suffix (36 chars: 32 hex + 4 hyphens).
	uuidPart := rest[roleLen+1:]
	if len(uuidPart) != 36 {
		t.Errorf("String() = %q: uuid suffix %q has length %d, want 36", s, uuidPart, len(uuidPart))
	}
}

func TestParseEphemeralClientID_Valid(t *testing.T) {
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
			assertEphemeralClientIDValid(t, tc.cellID, tc.role)
		})
	}
}

func TestParseEphemeralClientID_InvalidCellID(t *testing.T) {
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
			_, err := ParseEphemeralClientID(tc.cellID, tc.role)
			if err == nil {
				t.Errorf("ParseEphemeralClientID(%q, %q) expected error, got nil", tc.cellID, tc.role)
			}
		})
	}
}

func TestParseEphemeralClientID_InvalidRole(t *testing.T) {
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
			_, err := ParseEphemeralClientID("mycell", tc.role)
			if err == nil {
				t.Errorf("ParseEphemeralClientID(%q, %q) expected error, got nil", "mycell", tc.role)
			}
		})
	}
}

func TestParseEphemeralClientID_Uniqueness(t *testing.T) {
	cid1, err1 := ParseEphemeralClientID("mycell", "pub")
	cid2, err2 := ParseEphemeralClientID("mycell", "pub")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if cid1.String() == cid2.String() {
		t.Error("two ParseEphemeralClientID calls returned identical values — uuid suffix must differ")
	}
}

func TestParseStableClientID_Valid(t *testing.T) {
	tests := []struct {
		name       string
		cellID     string
		role       string
		instanceID string
	}{
		{"operator-supplied", "mycell", "pub", "instance-01"},
		{"single-digit", "cell", "role", "1"},
		{"uuid-shaped", "cell", "role", "deadbeef-1234-5678-9abc-def012345678"},
		{"long-alpha", "cell", "role", "node-pod-us-east-1a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cid, err := ParseStableClientID(tc.cellID, tc.role, tc.instanceID)
			if err != nil {
				t.Fatalf("ParseStableClientID(%q,%q,%q) unexpected error: %v",
					tc.cellID, tc.role, tc.instanceID, err)
			}
			want := tc.cellID + "-" + tc.role + "-" + tc.instanceID
			if cid.String() != want {
				t.Errorf("String() = %q, want %q", cid.String(), want)
			}
		})
	}
}

func TestParseStableClientID_Stability(t *testing.T) {
	a, errA := ParseStableClientID("mycell", "pub", "instance-01")
	b, errB := ParseStableClientID("mycell", "pub", "instance-01")
	if errA != nil || errB != nil {
		t.Fatalf("unexpected errors: %v, %v", errA, errB)
	}
	if a.String() != b.String() {
		t.Errorf("ParseStableClientID must be deterministic: got %q vs %q", a.String(), b.String())
	}
}

func TestParseStableClientID_InvalidInstance(t *testing.T) {
	tests := []struct {
		name       string
		instanceID string
	}{
		{"empty", ""},
		{"uppercase", "Instance01"},
		{"underscore", "instance_01"},
		{"space", "instance 01"},
		{"trailing-hyphen", "instance-"},
		{"double-hyphen", "ins--01"},
		{"too-long", strings.Repeat("a", 65)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStableClientID("mycell", "pub", tc.instanceID)
			if err == nil {
				t.Errorf("ParseStableClientID instanceID=%q expected error, got nil", tc.instanceID)
			}
		})
	}
}

func TestClientID_ZeroValueString(t *testing.T) {
	var cid ClientID
	if cid.String() != "" {
		t.Errorf("zero-value ClientID.String() = %q, want empty string", cid.String())
	}
}

func TestClientID_SealedConstruction(t *testing.T) {
	// This test documents that ClientID{} produces a zero-value (empty String).
	// The only way to get a non-zero ClientID is ParseEphemeralClientID or
	// ParseStableClientID. We cannot literally test "this fails to compile"
	// but we confirm the zero value is distinguishable from a valid ClientID.
	var zero ClientID
	valid, _ := ParseEphemeralClientID("cell", "role")
	if zero.String() == valid.String() {
		t.Error("zero ClientID and parsed ClientID must have different String() values")
	}
}

func TestParseClientID_TotalLengthLimit(t *testing.T) {
	// cellID(32) + "-" + role(32) + "-" + uuid(36) = 102, well within 128.
	longCellID := strings.Repeat("a", 32)
	longRole := strings.Repeat("b", 32)
	_, err := ParseEphemeralClientID(longCellID, longRole)
	if err != nil {
		t.Errorf("ParseEphemeralClientID with total 102 chars unexpected error: %v", err)
	}
	// Stable form with 64-char instanceID: 32+1+32+1+64 = 130 > 128 → rejected.
	longInstance := strings.Repeat("c", 64)
	if _, err := ParseStableClientID(longCellID, longRole, longInstance); err == nil {
		t.Error("ParseStableClientID with total >128 chars should fail, got nil")
	}
}
