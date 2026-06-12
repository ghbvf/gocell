package authz

import "testing"

func TestPermission_String(t *testing.T) {
	if got := PermAuditRead().String(); got != "audit:read" {
		t.Fatalf("PermAuditRead().String() = %q, want %q", got, "audit:read")
	}
	if got := PermSystemRead().String(); got != "system:read" {
		t.Fatalf("PermSystemRead().String() = %q, want %q", got, "system:read")
	}
}

// TestPermSystemRead_StableIdentity pins that the accessor returns the closed
// registry's private singleton (same F6 immutability contract as PermAuditRead).
func TestPermSystemRead_StableIdentity(t *testing.T) {
	if PermSystemRead() != permSystemRead {
		t.Fatal("PermSystemRead() must return the package-private singleton (stable on every call)")
	}
	if PermSystemRead().IsZero() {
		t.Fatal("minted PermSystemRead() must report IsZero()==false")
	}
}

// TestPermAuditRead_StableIdentity pins that the accessor returns the closed
// registry's private singleton (the F6 immutability contract: an external package
// cannot reassign or fork the registry value because PermAuditRead is a function,
// not a reassignable var; returning the singleton makes every call stable).
func TestPermAuditRead_StableIdentity(t *testing.T) {
	got := PermAuditRead()
	if got != permAuditRead {
		t.Fatal("PermAuditRead() must return the package-private singleton (stable on every call)")
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
	if PermAuditRead().IsZero() {
		t.Fatal("minted PermAuditRead() must report IsZero()==false")
	}
}

func TestPermissions_ClosedRegistry(t *testing.T) {
	perms := Permissions()
	// Pin the closed set. A new Perm* var that forgets to enroll in
	// allPermissions (or an accidental extra) trips this — the anti-vacuity guard
	// for the closed registry. Current set: audit:read (PR-10a), system:read (#1860).
	if len(perms) != 2 {
		t.Fatalf("Permissions() len = %d, want 2 (audit:read, system:read)", len(perms))
	}
	want := map[string]bool{"audit:read": true, "system:read": true}
	for _, p := range perms {
		if !want[p.String()] {
			t.Fatalf("unexpected permission in registry: %q", p.String())
		}
		delete(want, p.String())
	}
	if len(want) != 0 {
		t.Fatalf("registry missing expected permissions: %v", want)
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

// TestPermissions_KnownVarsEnrolled asserts that each Perm* accessor exported by
// this package is present in Permissions(). A new perm* var whose accessor is
// declared but NOT added to allPermissions is caught here (registry-enrollment
// contract).
//
// Note: Go does not support reflect-based enumeration of package-level vars from
// within the same package's test, so this test enumerates the known accessors
// explicitly. The anti-vacuity property comes from TestPermissions_ClosedRegistry
// (which pins len==1 for PR-10a) — together they ensure no var is silently
// un-enrolled: a new perm* must be added to allPermissions (or len test fails)
// AND its accessor must appear in the explicit membership check below.
//
// Registry-enrollment contract: every new perm* var added to permission.go MUST
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

	// Enumerate every exported Perm* accessor. Update this list when adding new vars.
	knownVars := []struct {
		name string
		perm Permission
	}{
		{"PermAuditRead", PermAuditRead()},
		{"PermSystemRead", PermSystemRead()},
	}

	for _, kv := range knownVars {
		if !contains(kv.perm) {
			t.Errorf("%s (%q) is declared but missing from Permissions() — add it to allPermissions in permission.go",
				kv.name, kv.perm.String())
		}
	}
}
