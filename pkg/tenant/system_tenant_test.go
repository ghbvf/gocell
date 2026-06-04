package tenant_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// TestSystemTenantID_ValidateAcceptsButParseTenantIDRejects documents the
// deliberate split between Validate and ParseTenantID for the nil-UUID sentinel:
//
//   - Validate(): ACCEPTS SystemTenantID — it is a valid canonical TenantID
//     value used by internal repo code paths that pass the const directly.
//   - ParseTenantID(): REJECTS the nil-UUID string — parse is the untrusted-input
//     boundary (JWT claim, X-Tenant-ID header, UnmarshalJSON); a public caller
//     must never be able to alias the system-tier config by submitting the nil-UUID.
//
// This split is the F2 security fix: the sentinel is valid for internal use
// but must not be obtainable from external string input.
func TestSystemTenantID_ValidateAcceptsButParseTenantIDRejects(t *testing.T) {
	t.Parallel()

	// Validate MUST accept SystemTenantID — repos call Validate on the typed param.
	if err := tenant.SystemTenantID.Validate(); err != nil {
		t.Fatalf("SystemTenantID.Validate() = %v, want nil (sentinel must be a valid canonical TenantID)", err)
	}

	// ParseTenantID MUST reject the nil-UUID string (F2 security fix).
	// Untrusted callers (JWT claim, X-Tenant-ID header) must not be able to
	// alias the system-tier config by submitting the nil-UUID string.
	_, err := tenant.ParseTenantID(string(tenant.SystemTenantID))
	if err == nil {
		t.Fatal("ParseTenantID(SystemTenantID) = nil error, want non-nil: " +
			"the reserved nil-UUID must be rejected from untrusted-input paths")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("ParseTenantID(SystemTenantID) error = %q, want it to mention 'reserved'", err.Error())
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
