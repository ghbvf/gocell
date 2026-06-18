package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

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

// TestRegistrycorePermissions pins the exact action spelling and singleton
// identity of every registrycore permission minted in 303-US4 (#2235). The action
// string IS the wire value carried into the PDP and matched against baseline rule
// Action targets, so a typo here silently breaks the gate↔baseline binding.
func TestRegistrycorePermissions(t *testing.T) {
	cases := []struct {
		name      string
		got       Permission
		singleton Permission
		want      string
	}{
		{"PermRegistrySubmit", PermRegistrySubmit(), permRegistrySubmit, "registry:submit"},
		{"PermRegistryRead", PermRegistryRead(), permRegistryRead, "registry:read"},
	}
	for _, tc := range cases {
		if got := tc.got.String(); got != tc.want {
			t.Errorf("%s.String() = %q, want %q", tc.name, got, tc.want)
		}
		if tc.got != tc.singleton {
			t.Errorf("%s() must return the package-private singleton (stable on every call)", tc.name)
		}
		if tc.got.IsZero() {
			t.Errorf("minted %s() must report IsZero()==false", tc.name)
		}
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

// TestAccesscorePermissions_String pins the exact action spelling of every
// accesscore permission minted in PR-10c. The action string IS the wire value
// carried into the PDP and matched against baseline rule Action targets, so a typo
// here silently breaks the gate↔baseline binding.
func TestAccesscorePermissions_String(t *testing.T) {
	cases := []struct {
		name string
		perm Permission
		want string
	}{
		{"PermPolicyRead", PermPolicyRead(), "policy:read"},
		{"PermPolicyWrite", PermPolicyWrite(), "policy:write"},
		{"PermUserRead", PermUserRead(), "user:read"},
		{"PermUserWrite", PermUserWrite(), "user:write"},
		{"PermRoleRead", PermRoleRead(), "role:read"},
		{"PermAccessDecide", PermAccessDecide(), "access:decide"},
		{"PermSessionVerify", PermSessionVerify(), "session:verify"},
	}
	for _, tc := range cases {
		if got := tc.perm.String(); got != tc.want {
			t.Errorf("%s.String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestExamplePermissions_String pins the exact action spelling of every
// examples permission minted in PR-10d (#1894). The action string IS the wire
// value carried into the example's own PDP and matched against its baseline rule
// actions, so a typo here silently breaks the gate↔baseline binding.
func TestExamplePermissions_String(t *testing.T) {
	cases := []struct {
		name string
		perm Permission
		want string
	}{
		{"PermDeviceCommand", PermDeviceCommand(), "device:command"},
		{"PermDeviceConsume", PermDeviceConsume(), "device:consume"},
		{"PermDeviceRead", PermDeviceRead(), "device:read"},
		{"PermDeviceList", PermDeviceList(), "device:list"},
		{"PermOrderCreate", PermOrderCreate(), "order:create"},
		{"PermOrderList", PermOrderList(), "order:list"},
		{"PermOrderRead", PermOrderRead(), "order:read"},
		{"PermOrderUpdate", PermOrderUpdate(), "order:update"},
	}
	for _, tc := range cases {
		if got := tc.perm.String(); got != tc.want {
			t.Errorf("%s.String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestExamplePermissions_StableIdentity pins that each examples accessor returns
// the same package-private singleton on every call (the F6 immutability contract:
// function accessors are not reassignable, so the registry value is immutable to
// external packages).
func TestExamplePermissions_StableIdentity(t *testing.T) {
	cases := []struct {
		name      string
		got       Permission
		singleton Permission
	}{
		{"PermDeviceCommand", PermDeviceCommand(), permDeviceCommand},
		{"PermDeviceConsume", PermDeviceConsume(), permDeviceConsume},
		{"PermDeviceRead", PermDeviceRead(), permDeviceRead},
		{"PermDeviceList", PermDeviceList(), permDeviceList},
		{"PermOrderCreate", PermOrderCreate(), permOrderCreate},
		{"PermOrderList", PermOrderList(), permOrderList},
		{"PermOrderRead", PermOrderRead(), permOrderRead},
		{"PermOrderUpdate", PermOrderUpdate(), permOrderUpdate},
	}
	for _, tc := range cases {
		if tc.got != tc.singleton {
			t.Errorf("%s() must return the package-private singleton (stable on every call)", tc.name)
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

// TestConfigcorePermissions_StableIdentity pins that each configcore accessor
// returns the same package-private singleton on every call (the F6 immutability
// contract from PR-10b: function accessors are not reassignable, so the registry
// value is immutable to external packages; returning the singleton on every call
// is the observable proof of that invariant within the package itself).
func TestConfigcorePermissions_StableIdentity(t *testing.T) {
	cases := []struct {
		name      string
		got       Permission
		singleton Permission
	}{
		{"PermConfigRead", PermConfigRead(), permConfigRead},
		{"PermConfigWrite", PermConfigWrite(), permConfigWrite},
		{"PermConfigPublish", PermConfigPublish(), permConfigPublish},
		{"PermFlagRead", PermFlagRead(), permFlagRead},
		{"PermFlagWrite", PermFlagWrite(), permFlagWrite},
	}
	for _, tc := range cases {
		if tc.got != tc.singleton {
			t.Errorf("%s() must return the package-private singleton (stable on every call)", tc.name)
		}
	}
}

// TestAccesscorePermissions_StableIdentity pins that each accesscore accessor
// returns the same package-private singleton on every call (the F6 immutability
// contract: function accessors are not reassignable, so the registry value is
// immutable to external packages).
func TestAccesscorePermissions_StableIdentity(t *testing.T) {
	cases := []struct {
		name      string
		got       Permission
		singleton Permission
	}{
		{"PermPolicyRead", PermPolicyRead(), permPolicyRead},
		{"PermPolicyWrite", PermPolicyWrite(), permPolicyWrite},
		{"PermUserRead", PermUserRead(), permUserRead},
		{"PermUserWrite", PermUserWrite(), permUserWrite},
		{"PermRoleRead", PermRoleRead(), permRoleRead},
		{"PermAccessDecide", PermAccessDecide(), permAccessDecide},
		{"PermSessionVerify", PermSessionVerify(), permSessionVerify},
	}
	for _, tc := range cases {
		if tc.got != tc.singleton {
			t.Errorf("%s() must return the package-private singleton (stable on every call)", tc.name)
		}
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
	// Pin the closed set. A new Perm* var that forgets to enroll in allPermissions
	// (or an accidental extra) trips this — the anti-vacuity guard for the closed
	// registry. Current set: audit:read (PR-10a) + system:read (#1860) + 5 configcore
	// (PR-10b) + 7 accesscore (PR-10c + session:verify #1154 + access:decide #1863) +
	// 2 registrycore (303-US4 #2235) + 4 iotdevice + 4 todoorder (PR-10d #1894).
	if len(perms) != 24 {
		t.Fatalf("Permissions() len = %d, want 24 "+
			"(2 platform + 5 configcore + 7 accesscore + 2 registrycore + 4 iotdevice + 4 todoorder)", len(perms))
	}
	want := map[string]bool{
		"audit:read": true, "system:read": true,
		"config:read": true, "config:write": true, "config:publish": true,
		"flag:read": true, "flag:write": true,
		"policy:read": true, "policy:write": true,
		"user:read": true, "user:write": true, "role:read": true,
		"access:decide": true, "session:verify": true,
		"registry:submit": true, "registry:read": true,
		"device:command": true, "device:consume": true, "device:read": true, "device:list": true,
		"order:create": true, "order:list": true, "order:read": true, "order:update": true,
	}
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

// TestPermissions_AccessorsEnrolled SELF-DISCOVERS every exported Perm*() accessor
// in permission.go (via go/parser, not a hand-maintained list) and asserts the set
// of actions they expose equals the closed registry returned by Permissions() —
// bidirectionally:
//
//   - every accessor's action must be in Permissions() (no accessor whose singleton
//     was forgotten from allPermissions — the F4 double-omission the old hand-written
//     knownVars list could silently miss);
//   - every Permissions() action must have an accessor (no registry entry without a
//     public accessor).
//
// Because the accessors are discovered from source, adding a new Perm*() without
// enrolling its singleton now fails automatically — no test edit required.
//
// AI-robust grade: Medium (machine-decidable static AST scan, self-discovering). A
// Hard alternative — generate the Perm*() accessors and allPermissions from one
// registry table + golden — would need a new `gocell generate authz` command, which
// is NOT a low-cost Hard path, so per ai-robust.md no follow-up issue is filed and
// this Medium guard stands. TestPermissions_ClosedRegistry keeps the len anchor.
func TestPermissions_AccessorsEnrolled(t *testing.T) {
	accessorActions := parsePermissionAccessorActions(t) // action -> accessor name

	// Anti-vacuity: a parser regression that finds nothing must fail, not pass.
	if len(accessorActions) < len(Permissions()) {
		t.Fatalf("self-discovery found %d Perm*() accessors in permission.go, want ≥ len(Permissions()) (%d) "+
			"— the parser likely regressed", len(accessorActions), len(Permissions()))
	}

	registry := map[string]struct{}{}
	for _, p := range Permissions() {
		registry[p.String()] = struct{}{}
	}

	for action, fn := range accessorActions {
		if _, ok := registry[action]; !ok {
			t.Errorf("accessor %s() exposes %q which is NOT in Permissions() — "+
				"append its singleton to allPermissions in permission.go", fn, action)
		}
	}
	for action := range registry {
		if _, ok := accessorActions[action]; !ok {
			t.Errorf("registry action %q has no exported Perm*() accessor in permission.go", action)
		}
	}
}

// parsePermissionAccessorActions parses permission.go and returns a map from each
// exported Perm*() accessor's action string to the accessor name, by statically
// resolving `func PermX() Permission { return permX }` →
// `var permX = newPermission("action")`. It is the self-discovery engine behind
// TestPermissions_AccessorsEnrolled.
func parsePermissionAccessorActions(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "permission.go", nil, 0)
	if err != nil {
		t.Fatalf("parse permission.go: %v", err)
	}

	varAction := map[string]string{}   // perm var name -> action literal
	accessorVar := map[string]string{} // Perm*() name   -> returned var name
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if action, ok := newPermissionLiteral(vs.Values[i]); ok {
							varAction[name.Name] = action
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv != nil || !strings.HasPrefix(d.Name.Name, "Perm") || !returnsPermission(d.Type) {
				continue
			}
			if v, ok := singleReturnIdent(d.Body); ok {
				accessorVar[d.Name.Name] = v
			}
		}
	}

	out := map[string]string{}
	for fn, varName := range accessorVar {
		action, ok := varAction[varName]
		if !ok {
			t.Errorf("accessor %s() returns %q, which is not a newPermission(...) singleton in permission.go", fn, varName)
			continue
		}
		out[action] = fn
	}
	return out
}

// newPermissionLiteral reports the action string of a `newPermission("action")`
// call expression.
func newPermissionLiteral(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || fn.Name != "newPermission" || len(call.Args) != 1 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// TestPermissionByName_ResolvesClosedSet pins that PermissionByName round-trips
// every action string in the closed registry to its sealed singleton (identity,
// not a fresh value). This is the string→Permission resolver the gRPC method
// permission overlay relies on (#2008): a contract carries the action string
// (e.g. "device:command"), and the registrar resolves it back to the sealed
// Permission. Resolving (not minting) preserves the seal — there is still no way
// to construct a Permission outside this file's registry.
func TestPermissionByName_ResolvesClosedSet(t *testing.T) {
	for _, p := range Permissions() {
		got, ok := PermissionByName(p.String())
		if !ok {
			t.Errorf("PermissionByName(%q) ok=false, want true", p.String())
			continue
		}
		if got != p {
			t.Errorf("PermissionByName(%q) returned a non-identical Permission; "+
				"must return the closed-registry singleton", p.String())
		}
	}
}

// TestPermissionByName_Unknown pins fail-closed resolution: an unknown or empty
// action string yields the zero Permission + ok=false, so a typo'd contract
// overlay cannot resolve to a valid gate (the registrar fails fast on ok=false).
func TestPermissionByName_Unknown(t *testing.T) {
	cases := []string{"", "nope:read", "device:commandx", "DEVICE:COMMAND", "admin"}
	for _, s := range cases {
		got, ok := PermissionByName(s)
		if ok {
			t.Errorf("PermissionByName(%q) ok=true, want false (unknown)", s)
		}
		if !got.IsZero() {
			t.Errorf("PermissionByName(%q) returned non-zero Permission for unknown string", s)
		}
	}
}

// TestIsKnownPermissionString pins the static closed-set predicate FMT-41 uses to
// reject a typo'd permission in a gRPC method overlay at validate time.
func TestIsKnownPermissionString(t *testing.T) {
	if !IsKnownPermissionString("device:command") {
		t.Error("IsKnownPermissionString(\"device:command\") = false, want true")
	}
	for _, s := range []string{"", "nope:read", "DEVICE:COMMAND"} {
		if IsKnownPermissionString(s) {
			t.Errorf("IsKnownPermissionString(%q) = true, want false", s)
		}
	}
}

// returnsPermission reports whether ft is the signature `() Permission`.
func returnsPermission(ft *ast.FuncType) bool {
	if ft.Params != nil && len(ft.Params.List) != 0 {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	id, ok := ft.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "Permission"
}

// singleReturnIdent reports the identifier name of a body that is exactly
// `{ return someIdent }`.
func singleReturnIdent(body *ast.BlockStmt) (string, bool) {
	if body == nil || len(body.List) != 1 {
		return "", false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	id, ok := ret.Results[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}
