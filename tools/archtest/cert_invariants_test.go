//go:build archtest

// INVARIANT: CERT-VALUE-SEALED-CONSTRUCTION-01
// INVARIANT: CERT-SIGN-FUNNEL-01
// INVARIANT: CERT-REVOKE-SCOPED-01
//
// Consolidated certificate-signing archtest invariants for
// framework/runtime/certsigning (Epic #1895 PR-5 #1901). Authoritative godoc:
// framework/runtime/certsigning/doc.go §Enforced invariants.
//
// # CERT-VALUE-SEALED-CONSTRUCTION-01 (Hard)
//
// Every cross-trust-boundary input and minted-credential value type in
// certsigning has only unexported fields, so a POPULATED composite literal of
// any of them outside the package is a Go compile error — forging certificate
// material / signing inputs is structurally unrepresentable. The sole minters
// are the New* constructors (RevocationReason: the Reason* accessor singletons).
// The Go compiler is the Hard gate; these tests are the REVERSE self-check that
// pins the seal so a future PR re-exporting a field, or adding a second
// constructor surface, fails at PR/nightly time:
//   - AllFieldsUnexported (reflect): every field of every sealed type is unexported.
//   - SoleConstructionSurface (go/types): the exported funcs returning each
//     sealed type match the frozen constructor allowlist exactly (anti-vacuity).
//
// Read-only OUTPUT types (RevokedCertificate, the TrustBundle DER slice) are
// intentionally NOT sealed — they carry no forgeable boundary — and are absent
// from certSealedTypes by design.
//
// # CERT-SIGN-FUNNEL-01 (funnel; upstream Hard, downstream Medium)
//
// Upstream Hard: IssuedCert / CertRequest field sealing (above) makes forged
// signing material uncompilable. Downstream Medium: a caller-allowlist scan
// restricts who may invoke NewIssuedCert (the mint funnel) to the sanctioned
// signer adapter. The allowlist (certIssuedMintAllowlist) is EMPTY in PR-5 — no
// signer implementation exists yet; adapters/softca joins it in PR-6. GREEN =
// zero callers outside certsigning today; the RED fixture proves the detector
// fires.
//
// Blind spot (per AI-robust §强制盲区自检): NewIssuedCert MUST be exported
// because the signing adapter lives in a different module, so "only the
// sanctioned Signer mints certificates" is a Medium caller-allowlist, NOT Hard —
// there is no low-cost Hard path while the adapter is necessarily external. The
// upstream populated-literal forgery vector cannot have a RED fixture: such a
// literal does not compile (that is precisely the Hard seal).
//
// # CERT-REVOKE-SCOPED-01 (Hard)
//
// RevocationStore.Revoke / RevocationList / Tidy take CertScope (and Revoke
// additionally Serial) as mandatory typed positional parameters — omitting the
// scope is a compile error (arity), passing a raw string is a compile error
// (type). The type system is the Hard guarantee; this is the reverse
// self-check / anti-vacuity that fails if a refactor relaxes a signature back to
// a bare string or serial, or drops a required method.
package archtest

import (
	"go/ast"
	"go/types"
	"reflect"
	"testing"

	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// certSigningPkgPath is the import path of the package under guard.
func certSigningPkgPath() string { return PlatformFrameworkModulePath + "/runtime/certsigning" }

const certSigningPattern = "./framework/runtime/certsigning/..."

// ── CERT-VALUE-SEALED-CONSTRUCTION-01 ──────────────────────────────────────

// certSealedTypes is the closed set of sealed (unexported-field) value types.
// Read-only output types (RevokedCertificate, trust-bundle slice) are excluded
// by design — they carry no forgeable boundary.
var certSealedTypes = []struct {
	name string
	typ  reflect.Type
}{
	{"IssuerID", reflect.TypeOf(cs.IssuerID{})},
	{"DeviceID", reflect.TypeOf(cs.DeviceID{})},
	{"Serial", reflect.TypeOf(cs.Serial{})},
	{"CertScope", reflect.TypeOf(cs.CertScope{})},
	{"SubjectAltNames", reflect.TypeOf(cs.SubjectAltNames{})},
	{"KeyUsages", reflect.TypeOf(cs.KeyUsages{})},
	{"DeviceSubject", reflect.TypeOf(cs.DeviceSubject{})},
	{"CertRequest", reflect.TypeOf(cs.CertRequest{})},
	{"IssuedCert", reflect.TypeOf(cs.IssuedCert{})},
	{"AuthorizedCertRequest", reflect.TypeOf(cs.AuthorizedCertRequest{})},
	{"EnrollmentClaim", reflect.TypeOf(cs.EnrollmentClaim{})},
	{"SignConstraints", reflect.TypeOf(cs.SignConstraints{})},
	{"RevocationReason", reflect.TypeOf(cs.RevocationReason{})},
}

// certConstructorSurface freezes, per sealed type, the exact set of exported
// package-level funcs that may return it. A drift (new constructor, or a removed
// one) fails the SoleConstructionSurface check.
var certConstructorSurface = map[string]map[string]struct{}{
	"IssuerID":              {"NewIssuerID": {}},
	"DeviceID":              {"NewDeviceID": {}},
	"Serial":                {"NewSerial": {}},
	"CertScope":             {"NewCertScope": {}},
	"SubjectAltNames":       {"NewSubjectAltNames": {}},
	"KeyUsages":             {"NewKeyUsages": {}},
	"DeviceSubject":         {"NewDeviceSubject": {}},
	"CertRequest":           {"NewCertRequest": {}},
	"IssuedCert":            {"NewIssuedCert": {}},
	"AuthorizedCertRequest": {"NewAuthorizedCertRequest": {}},
	"EnrollmentClaim":       {"NewEnrollmentClaim": {}},
	"SignConstraints":       {"NewSignConstraints": {}},
	"RevocationReason": {
		"ReasonUnspecified": {}, "ReasonKeyCompromise": {}, "ReasonCACompromise": {},
		"ReasonAffiliationChanged": {}, "ReasonSuperseded": {}, "ReasonCessationOfOperation": {},
		"ReasonCertificateHold": {}, "ReasonRemoveFromCRL": {}, "ReasonPrivilegeWithdrawn": {},
		"ReasonAACompromise": {},
	},
}

// TestCertValueSealedConstruction01_AllFieldsUnexported reflectively asserts
// every sealed type has zero exported fields — the property that makes a
// populated composite literal a compile error outside certsigning.
func TestCertValueSealedConstruction01_AllFieldsUnexported(t *testing.T) {
	t.Parallel()
	for _, st := range certSealedTypes {
		if st.typ.Kind() != reflect.Struct {
			t.Errorf("CERT-VALUE-SEALED-CONSTRUCTION-01: certsigning.%s is not a struct (kind=%s)", st.name, st.typ.Kind())
			continue
		}
		if st.typ.NumField() == 0 {
			t.Errorf("CERT-VALUE-SEALED-CONSTRUCTION-01: certsigning.%s has no fields — the seal would be vacuous", st.name)
			continue
		}
		for i := 0; i < st.typ.NumField(); i++ {
			f := st.typ.Field(i)
			if f.IsExported() {
				t.Errorf("CERT-VALUE-SEALED-CONSTRUCTION-01: certsigning.%s field %q is EXPORTED — this re-opens "+
					"external populated-literal forgery of certificate material. Keep all fields unexported and "+
					"expose reads via value-receiver getters; construct via the New* funnel.", st.name, f.Name)
			}
		}
	}
}

// TestCertValueSealedConstruction01_SoleConstructionSurface pins, per sealed
// type, the complete set of exported funcs that yield it. A new func returning a
// sealed type (a fresh construction backdoor the literal seal does not cover), or
// a removed sanctioned constructor (making the seal vacuous), fails here.
func TestCertValueSealedConstruction01_SoleConstructionSurface(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	pkgPath := certSigningPkgPath()
	var visited bool
	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{certSigningPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != pkgPath {
				return nil
			}
			visited = true
			scope := p.Pkg.Scope()
			var out []Diagnostic
			for typeName, expected := range certConstructorSurface {
				obj := scope.Lookup(typeName)
				if obj == nil {
					out = append(out, Diagnostic{Message: "CERT-VALUE-SEALED-CONSTRUCTION-01: sealed type certsigning." + typeName + " not found"})
					continue
				}
				want := obj.Type()
				var funcs []string
				for _, name := range scope.Names() {
					o := scope.Lookup(name)
					if !o.Exported() {
						continue
					}
					fn, ok := o.(*types.Func)
					if !ok {
						continue
					}
					if sig, ok := fn.Type().(*types.Signature); ok && sigReturnsType(sig, want) {
						funcs = append(funcs, name)
					}
				}
				out = append(out, diffExpectedSet("constructors returning certsigning."+typeName, expected, funcs)...)
			}
			return out
		})
	Report(t, "CERT-VALUE-SEALED-CONSTRUCTION-01/SoleConstructionSurface", diags)
	if !visited {
		t.Fatal("CERT-VALUE-SEALED-CONSTRUCTION-01/SoleConstructionSurface: certsigning package was never " +
			"scanned (Typed load returned no matching package) — the check is vacuous; fix the load pattern")
	}
}

// ── CERT-SIGN-FUNNEL-01 ────────────────────────────────────────────────────

// certIssuedMintAllowlist is the set of package import paths permitted to call
// certsigning.NewIssuedCert (the certificate mint funnel). EMPTY in PR-5: no
// signer implementation exists yet. adapters/softca (PR-6 #1902) is the first
// member. The certsigning package itself is exempt separately (it is the home of
// the constructor).
var certIssuedMintAllowlist = map[string]struct{}{}

const certMintCallerMsg = "forbidden call certsigning.NewIssuedCert — minting an IssuedCert is restricted to the " +
	"sanctioned signer adapter (certIssuedMintAllowlist); business code obtains certificates via Signer.Sign"

// scanCertMintCallers flags calls to certsigning.NewIssuedCert from packages
// outside certsigning and the mint allowlist.
func scanCertMintCallers(p *Pass, pkgPath string) []Diagnostic {
	if p.Pkg != nil && p.Pkg.Path() == pkgPath {
		return nil
	}
	if p.Pkg != nil {
		if _, ok := certIssuedMintAllowlist[p.Pkg.Path()]; ok {
			return nil
		}
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if IsCallToPkgFunc(p.TypesInfo, call, pkgPath, "NewIssuedCert") {
				pos := p.Fset.Position(call.Pos())
				diags = append(diags, Diagnostic{Rel: rel, Line: pos.Line, Message: certMintCallerMsg})
			}
		})
	}
	return diags
}

// TestCertSignFunnel01_GreenProduction asserts production code has zero
// out-of-allowlist callers of certsigning.NewIssuedCert today.
func TestCertSignFunnel01_GreenProduction(t *testing.T) {
	t.Parallel()
	pkgPath := certSigningPkgPath()
	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		return scanCertMintCallers(p, pkgPath)
	})
	if len(diags) > 0 {
		t.Errorf("CERT-SIGN-FUNNEL-01 production GREEN: expected 0 violations, got %d:\n%v", len(diags), diags)
	}
}

// TestCertSignFunnel01_RedFixture loads the archtest_fixture-tagged fixture (a
// non-allowlisted package calling NewIssuedCert) and asserts the detector fires
// — the reverse self-check that the GREEN baseline above is meaningful.
func TestCertSignFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	pkgPath := certSigningPkgPath()
	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/certsignmintfixture/..."}),
		func(p *Pass) []Diagnostic {
			return scanCertMintCallers(p, pkgPath)
		})
	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	var hits int
	for _, d := range diags {
		if d.Message == certMintCallerMsg {
			hits++
		}
	}
	if hits == 0 {
		t.Error("CERT-SIGN-FUNNEL-01 RED fixture: scanner found 0 hits; expected ≥ 1 from " +
			"forbidden_mint_caller.go — the detector scanCertMintCallers may be broken")
	}
}

// TestCertSignFunnel01_SignTakesAuthorizedRequest is the reverse self-check that
// Signer.Sign accepts a sealed AuthorizedCertRequest (NOT a bare CertRequest).
// Taking AuthorizedCertRequest is the compile-time guarantee that authorization
// + constraint enforcement (NewAuthorizedCertRequest: Granted / TTL / SAN) has
// happened before signing; relaxing the param back to CertRequest would re-open
// unauthorized signing (FR-005) and is caught here.
func TestCertSignFunnel01_SignTakesAuthorizedRequest(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	pkgPath := certSigningPkgPath()
	var visited bool
	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{certSigningPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != pkgPath {
				return nil
			}
			visited = true
			scope := p.Pkg.Scope()
			signerObj := scope.Lookup("Signer")
			if signerObj == nil {
				return []Diagnostic{{Message: "CERT-SIGN-FUNNEL-01: Signer interface not found"}}
			}
			iface, ok := signerObj.Type().Underlying().(*types.Interface)
			if !ok {
				return []Diagnostic{{Message: "CERT-SIGN-FUNNEL-01: Signer is not an interface"}}
			}
			authType := lookupType(scope, "AuthorizedCertRequest")
			certReqType := lookupType(scope, "CertRequest")
			if authType == nil {
				return []Diagnostic{{Message: "CERT-SIGN-FUNNEL-01: AuthorizedCertRequest type not found"}}
			}
			var out []Diagnostic
			found := false
			for i := 0; i < iface.NumMethods(); i++ {
				m := iface.Method(i)
				if m.Name() != "Sign" {
					continue
				}
				found = true
				sig, ok := m.Type().(*types.Signature)
				if !ok {
					continue
				}
				if !sigHasParamType(sig, authType) {
					out = append(out, Diagnostic{Message: "CERT-SIGN-FUNNEL-01: Signer.Sign must take an " +
						"AuthorizedCertRequest parameter — authorization must precede signing (FR-005)"})
				}
				if certReqType != nil && sigHasParamType(sig, certReqType) {
					out = append(out, Diagnostic{Message: "CERT-SIGN-FUNNEL-01: Signer.Sign must NOT take a bare " +
						"CertRequest — use AuthorizedCertRequest so the authorization gate cannot be skipped"})
				}
			}
			if !found {
				out = append(out, Diagnostic{Message: "CERT-SIGN-FUNNEL-01: Signer.Sign method not found — the guard is vacuous"})
			}
			return out
		})
	Report(t, "CERT-SIGN-FUNNEL-01/SignTakesAuthorizedRequest", diags)
	if !visited {
		t.Fatal("CERT-SIGN-FUNNEL-01/SignTakesAuthorizedRequest: certsigning package was never scanned — vacuous")
	}
}

// ── CERT-REVOKE-SCOPED-01 ──────────────────────────────────────────────────

// TestCertRevokeScoped01_StoreMethodsCarryScope is the reverse self-check that
// RevocationStore.Revoke / RevocationList / Tidy still take CertScope (and Revoke
// additionally Serial) as typed positional parameters. The Hard guarantee is the
// compiler (omitting the scope is an arity error; a bare string a type error);
// this fails if a refactor relaxes a signature or drops a required method.
func TestCertRevokeScoped01_StoreMethodsCarryScope(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	pkgPath := certSigningPkgPath()
	var visited bool
	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{certSigningPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != pkgPath {
				return nil
			}
			visited = true
			scope := p.Pkg.Scope()
			storeObj := scope.Lookup("RevocationStore")
			if storeObj == nil {
				return []Diagnostic{{Message: "CERT-REVOKE-SCOPED-01: RevocationStore interface not found"}}
			}
			iface, ok := storeObj.Type().Underlying().(*types.Interface)
			if !ok {
				return []Diagnostic{{Message: "CERT-REVOKE-SCOPED-01: RevocationStore is not an interface"}}
			}
			certScopeType := lookupType(scope, "CertScope")
			serialType := lookupType(scope, "Serial")
			if certScopeType == nil || serialType == nil {
				return []Diagnostic{{Message: "CERT-REVOKE-SCOPED-01: CertScope / Serial type not found"}}
			}
			requireScope := map[string]bool{"Revoke": true, "RevocationList": true, "Tidy": true}
			requireSerial := map[string]bool{"Revoke": true}
			seen := map[string]bool{}
			var out []Diagnostic
			for i := 0; i < iface.NumMethods(); i++ {
				m := iface.Method(i)
				if !requireScope[m.Name()] {
					continue
				}
				seen[m.Name()] = true
				sig, ok := m.Type().(*types.Signature)
				if !ok {
					continue
				}
				if !sigHasParamType(sig, certScopeType) {
					out = append(out, Diagnostic{Message: "CERT-REVOKE-SCOPED-01: RevocationStore." + m.Name() +
						" does not take a certsigning.CertScope positional parameter — revocation must be scoped " +
						"(绝不凭裸 serial 跨隔离域)"})
				}
				if requireSerial[m.Name()] && !sigHasParamType(sig, serialType) {
					out = append(out, Diagnostic{Message: "CERT-REVOKE-SCOPED-01: RevocationStore." + m.Name() +
						" does not take a certsigning.Serial positional parameter — a bare string serial is forbidden"})
				}
			}
			for name := range requireScope {
				if !seen[name] {
					out = append(out, Diagnostic{Message: "CERT-REVOKE-SCOPED-01: required RevocationStore method " +
						name + " not found — the scope guard is now vacuous"})
				}
			}
			return out
		})
	Report(t, "CERT-REVOKE-SCOPED-01/StoreMethodsCarryScope", diags)
	if !visited {
		t.Fatal("CERT-REVOKE-SCOPED-01/StoreMethodsCarryScope: certsigning package was never scanned " +
			"(Typed load returned no matching package) — the check is vacuous; fix the load pattern")
	}
}

// lookupType returns the types.Type of a named type in scope, or nil.
func lookupType(scope *types.Scope, name string) types.Type {
	obj := scope.Lookup(name)
	if obj == nil {
		return nil
	}
	return obj.Type()
}

// sigHasParamType reports whether sig has any parameter whose type is identical
// to want.
func sigHasParamType(sig *types.Signature, want types.Type) bool {
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if types.Identical(params.At(i).Type(), want) {
			return true
		}
	}
	return false
}
