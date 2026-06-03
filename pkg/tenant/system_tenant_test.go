package tenant_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// TestSystemTenantID_IsValidCanonical is the reverse self-check guarding the
// load-bearing invariant of the SystemTenantID sentinel: the nil UUID MUST pass
// Validate() and round-trip through ParseTenantID unchanged. If a future edit to
// parseCanonical (e.g. a stricter "reject nil UUID" rule) breaks this, the
// sentinel would fail-closed at every internal config read — this test turns
// that regression red at the source.
func TestSystemTenantID_IsValidCanonical(t *testing.T) {
	t.Parallel()

	if err := tenant.SystemTenantID.Validate(); err != nil {
		t.Fatalf("SystemTenantID.Validate() = %v, want nil (sentinel must be a valid canonical TenantID)", err)
	}

	parsed, err := tenant.ParseTenantID(string(tenant.SystemTenantID))
	if err != nil {
		t.Fatalf("ParseTenantID(SystemTenantID) error = %v, want nil", err)
	}
	if parsed != tenant.SystemTenantID {
		t.Fatalf("ParseTenantID(SystemTenantID) = %q, want %q (must round-trip unchanged)", parsed, tenant.SystemTenantID)
	}
}

// TestSystemTenantID_Value pins the exact sentinel value (nil UUID). A change to
// the constant is a deliberate decision that should require updating this test —
// the value is referenced by the configreadinternal handler and is part of the
// tenant-isolation contract.
func TestSystemTenantID_Value(t *testing.T) {
	t.Parallel()

	const wantNilUUID = "00000000-0000-0000-0000-000000000000"
	if string(tenant.SystemTenantID) != wantNilUUID {
		t.Fatalf("SystemTenantID = %q, want %q", tenant.SystemTenantID, wantNilUUID)
	}
}
