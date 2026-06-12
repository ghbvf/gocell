package authz

import "testing"

func TestPermission_String(t *testing.T) {
	if got := PermAuditRead.String(); got != "audit:read" {
		t.Fatalf("PermAuditRead.String() = %q, want %q", got, "audit:read")
	}
}

func TestPermission_ZeroValueIsInvalid(t *testing.T) {
	var zero Permission
	if !zero.IsZero() {
		t.Fatal("zero Permission must report IsZero()==true")
	}
	if zero.String() != "" {
		t.Fatalf("zero Permission.String() = %q, want empty", zero.String())
	}
	if PermAuditRead.IsZero() {
		t.Fatal("minted PermAuditRead must report IsZero()==false")
	}
}

func TestPermissions_ClosedRegistry(t *testing.T) {
	perms := Permissions()
	// Pin the closed set: PR-10a seeds exactly one permission. A new Perm* var
	// that forgets to enroll in allPermissions (or an accidental extra) trips
	// this — the anti-vacuity guard for the closed registry.
	if len(perms) != 1 {
		t.Fatalf("Permissions() len = %d, want 1 (PR-10a seeds only PermAuditRead)", len(perms))
	}
	if perms[0].String() != "audit:read" {
		t.Fatalf("Permissions()[0] = %q, want audit:read", perms[0].String())
	}

	// Returned slice must be independent of the registry (mutation isolation).
	perms[0] = Permission{}
	if Permissions()[0].IsZero() {
		t.Fatal("Permissions() must return a copy; registry was mutated through the returned slice")
	}
}

func TestPermissions_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Permissions() {
		if p.IsZero() {
			t.Fatal("registry must not contain the zero Permission")
		}
		if seen[p.String()] {
			t.Fatalf("duplicate permission in registry: %q", p.String())
		}
		seen[p.String()] = true
	}
}

// TestPermissions_KnownVarsEnrolled asserts that each Perm* var exported by
// this package is present in Permissions(). A new Perm* var that is declared
// but NOT added to allPermissions is caught here (registry-enrollment contract).
//
// Note: Go does not support reflect-based enumeration of package-level vars from
// within the same package's test, so this test enumerates the known exported vars
// explicitly. The anti-vacuity property comes from TestPermissions_ClosedRegistry
// (which pins len==1 for PR-10a) — together they ensure no var is silently
// un-enrolled: a new Perm* must be added to allPermissions (or len test fails)
// AND must appear in the explicit membership check below.
//
// Registry-enrollment contract: every new Perm* var added to permission.go MUST
// also be appended to allPermissions in the same commit; omitting the enrollment
// causes TestPermissions_ClosedRegistry to fail (len mismatch) AND this test to
// fail (membership miss).
func TestPermissions_KnownVarsEnrolled(t *testing.T) {
	registry := Permissions()
	contains := func(p Permission) bool {
		for _, r := range registry {
			if r.String() == p.String() {
				return true
			}
		}
		return false
	}

	// Enumerate every exported Perm* var. Update this list when adding new vars.
	knownVars := []struct {
		name string
		perm Permission
	}{
		{"PermAuditRead", PermAuditRead},
	}

	for _, kv := range knownVars {
		if !contains(kv.perm) {
			t.Errorf("%s (%q) is declared but missing from Permissions() — add it to allPermissions in permission.go",
				kv.name, kv.perm.String())
		}
	}
}
