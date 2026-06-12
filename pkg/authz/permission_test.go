package authz

import "testing"

func TestPermission_String(t *testing.T) {
	if got := PermAuditRead().String(); got != "audit:read" {
		t.Fatalf("PermAuditRead().String() = %q, want %q", got, "audit:read")
	}
}

// TestConfigcorePermissions_String pins the exact action spelling of every
// configcore permission minted in PR-10b. The action string IS the wire value
// carried into the PDP (auth.Authorizer.Authorize) and matched against baseline
// rule Action targets, so a typo here silently breaks the gate↔baseline binding.
func TestConfigcorePermissions_String(t *testing.T) {
	cases := []struct {
		name string
		perm Permission
		want string
	}{
		{"PermConfigRead", PermConfigRead(), "config:read"},
		{"PermConfigWrite", PermConfigWrite(), "config:write"},
		{"PermConfigPublish", PermConfigPublish(), "config:publish"},
		{"PermFlagRead", PermFlagRead(), "flag:read"},
		{"PermFlagWrite", PermFlagWrite(), "flag:write"},
	}
	for _, tc := range cases {
		if got := tc.perm.String(); got != tc.want {
			t.Errorf("%s.String() = %q, want %q", tc.name, got, tc.want)
		}
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
	// Pin the closed set: PR-10a seeds exactly one permission. A new Perm* var
	// that forgets to enroll in allPermissions (or an accidental extra) trips
	// this — the anti-vacuity guard for the closed registry.
	if len(perms) != 6 {
		t.Fatalf("Permissions() len = %d, want 6 (PR-10a PermAuditRead + PR-10b configcore 5)", len(perms))
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
		{"PermConfigRead", PermConfigRead()},
		{"PermConfigWrite", PermConfigWrite()},
		{"PermConfigPublish", PermConfigPublish()},
		{"PermFlagRead", PermFlagRead()},
		{"PermFlagWrite", PermFlagWrite()},
	}

	for _, kv := range knownVars {
		if !contains(kv.perm) {
			t.Errorf("%s (%q) is declared but missing from Permissions() — add it to allPermissions in permission.go",
				kv.name, kv.perm.String())
		}
	}
}
