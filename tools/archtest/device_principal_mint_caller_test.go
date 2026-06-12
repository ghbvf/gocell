//go:build archtest

// device_principal_mint_caller_test.go — the in-package backstop for the SOLE
// sanctioned PrincipalDevice producer (#1898, epic #1895 PR-2).
//
// INVARIANT: DEVICE-PRINCIPAL-MINT-CALLER-01
//
// # What this guards
//
// runtime/auth.mintDevicePrincipal (deviceprincipal.go) is the only sanctioned
// site that mints a device principal. The PRIMARY enforcement is the type
// system: a PrincipalDevice only governs data visibility (RowVisibility →
// RowScopeDevice) when it carries the unexported Principal.device seal, which
// only mintDevicePrincipal can set and only the unexported newDeviceSeal can
// construct — so a forged auth.Principal{Kind: auth.PrincipalDevice} from any
// other package is type-inert (RowVisibility fails closed). That seal is the
// HARD gate (out-of-package forgery of a working device principal is not
// expressible).
//
// This archtest is the BACKSTOP: it pins every production construction of an
// auth.Principal composite literal with Kind == PrincipalDevice to a bounded
// allowlist (today: only deviceprincipal.go). It (a) documents intent
// machine-checkably, (b) catches a stray device-kind literal anywhere in the
// production tree (even though such a literal is inert without the seal), and
// (c) guards the in-package file discipline the type system cannot express
// (every runtime/auth file is in the same package and could, in principle,
// build a sealed device principal — a new in-package mint site must be added to
// the allowlist or CI goes red).
//
// # Legitimate producers (today)
//
//   - runtime/auth/deviceprincipal.go — mintDevicePrincipal, the sole sanctioned
//     device-principal issuer (sets Kind: PrincipalDevice + the device seal from
//     a verified device bearer token; PR-8's mTLS cert path will route through
//     the same minter).
//
// _test.go files and the archtest fixture are exempt (they construct device
// principals to exercise the type / the scanner): the Production scan excludes
// _test.go (Tests:false) and the build-tagged fixture.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (the seal gate in RowVisibility): HARD — type system. An
//     unsealed Principal{Kind: PrincipalDevice} cannot derive RowScopeDevice;
//     the device seal is an unexported field of an unexported type, unforgeable
//     out-of-package.
//   - Upstream (only this file mints): HARD at the package boundary (unexported
//     seal type + field); within runtime/auth it is MEDIUM, this archtest being
//     the file-discipline backstop. This is the intended split, not a deferred
//     TODO: Principal is an exported struct with an exported Kind enum (consumed
//     for metrics/logging/exhaustive-switch), so Go visibility cannot express
//     "only deviceprincipal.go may set Kind: PrincipalDevice" — the seal makes
//     the literal inert, and this scan keeps the literal where it belongs.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is constant-folded over the Kind field's compile-time value
//     (info.Types[value].Value vs the resolved value of auth.PrincipalDevice),
//     so a direct const, a cross-package selector, and an untyped const alias
//     are all caught; a Kind laundered through a non-constant local var/param is
//     NOT flagged by this backstop. That blind spot is harmless: such a literal
//     is still inert without the device seal (the Hard gate), and no production
//     site constructs a Principal with a dynamic Kind today.
//   - Detection is composite-literal based (auth.Principal{Kind: PrincipalDevice}).
//     A factory that returns a *Principal whose Kind is set field-by-field after
//     construction would evade the literal scan — but again, only the sealed
//     mint path produces a device principal that RowVisibility honors.
//   - The anti-vacuity guard (every allowlist entry must host ≥1 live device
//     literal) is the reverse self-check: it forbids a stale allowlist entry
//     becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// authRuntimePkgPath is the import path of runtime/auth (home of Principal and
// the PrincipalDevice kind constant).
const authRuntimePkgPath = PlatformModulePath + "/runtime/auth"

// devicePrincipalMintAllowlist is the set of module-relative production files
// allowed to construct an auth.Principal with Kind == PrincipalDevice. See the
// file godoc.
var devicePrincipalMintAllowlist = map[string]struct{}{
	"runtime/auth/deviceprincipal.go": {}, // mintDevicePrincipal — the sole sanctioned device-principal issuer
}

// devicePrincipalMintFixturePkg is the build-tagged RED fixture package exercised
// by the reverse self-check.
const devicePrincipalMintFixturePkg = "./tools/archtest/internal/deviceprincipalmintfixture"

// TestDevicePrincipalMintCaller01 asserts every production auth.Principal
// composite literal with Kind == PrincipalDevice sits in
// devicePrincipalMintAllowlist, and that every allowlist entry is live
// (anti-vacuity reverse check).
func TestDevicePrincipalMintCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		d, obs := checkDevicePrincipalMintCaller(p, devicePrincipalMintAllowlist)
		for f := range obs {
			observed[f] = struct{}{}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry must host
	// a live device-principal construction, else it is a dead bypass slot.
	allowed := make([]string, 0, len(devicePrincipalMintAllowlist))
	for f := range devicePrincipalMintAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"DEVICE-PRINCIPAL-MINT-CALLER-01: allowlist entry %q is STALE — no live "+
						"auth.Principal{Kind: PrincipalDevice} construction observed. Either the scanner "+
						"regressed or the sanctioned issuer was removed; drop the dead allowlist entry so it "+
						"cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "DEVICE-PRINCIPAL-MINT-CALLER-01", diags)
}

// TestDevicePrincipalMintCaller01_ScannerCatchesViolation is the reverse
// self-check: it runs the SAME production detector against the RED fixture
// (exercising the real allowlist + Diagnostic path, not a parallel counter),
// asserting it flags EXACTLY the fixture's device-principal literal and NOT the
// PrincipalUser control, and that an allowlisted run suppresses it.
func TestDevicePrincipalMintCaller01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Empty allowlist: the fixture's device-principal literal is flagged; the
	// PrincipalUser control is NOT.
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{devicePrincipalMintFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkDevicePrincipalMintCaller(p, nil)
		return d
	})

	const wantFlagged = 1 // ForgeDevicePrincipal only (MakeUserPrincipal is the GREEN control)
	if len(diags) != wantFlagged {
		t.Fatalf("DEVICE-PRINCIPAL-MINT-CALLER-01 scanner self-check: expected the production detector to "+
			"flag exactly the %d device-principal fixture construction (and NOT the PrincipalUser control), "+
			"got %d: %+v", wantFlagged, len(diags), diags)
	}
	for _, d := range diags {
		if !strings.HasSuffix(d.Rel, "deviceprincipalmintfixture/fixture.go") {
			t.Errorf("DEVICE-PRINCIPAL-MINT-CALLER-01 scanner self-check: diagnostic Rel %q is not the RED fixture file", d.Rel)
		}
		if !strings.Contains(d.Message, "DEVICE-PRINCIPAL-MINT-CALLER-01") {
			t.Errorf("DEVICE-PRINCIPAL-MINT-CALLER-01 scanner self-check: diagnostic missing rule ID: %q", d.Message)
		}
		if d.Line <= 0 {
			t.Errorf("DEVICE-PRINCIPAL-MINT-CALLER-01 scanner self-check: diagnostic has no resolved line: %+v", d)
		}
	}

	// Same detector, fixture file IN the allowlist → ZERO diagnostics. Proves the
	// allowlist-suppression branch is exercised on the identical path.
	allowFixture := map[string]struct{}{diags[0].Rel: {}}
	suppressed := Run(t, Fixture(FixtureOpts{Tests: false}, []string{devicePrincipalMintFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkDevicePrincipalMintCaller(p, allowFixture)
		return d
	})
	if len(suppressed) != 0 {
		t.Fatalf("DEVICE-PRINCIPAL-MINT-CALLER-01 scanner self-check: allowlisting the fixture file must "+
			"suppress all diagnostics on the same detector path, got %d: %+v", len(suppressed), suppressed)
	}
}

// checkDevicePrincipalMintCaller scans one typed Pass for production
// auth.Principal composite literals with Kind == PrincipalDevice, flagging any
// not in allowlist. It returns the diagnostics plus the set of module-relative
// files in which a device-principal construction was observed (for the
// anti-vacuity reverse check). It is the SINGLE detection path shared by the
// production test and the fixture self-check.
func checkDevicePrincipalMintCaller(p *Pass, allowlist map[string]struct{}) (diags []Diagnostic, observed map[string]struct{}) {
	observed = map[string]struct{}{}
	if !p.Typed() {
		return nil, observed
	}
	devVal := authPrincipalDeviceValue(p)
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
			if !isDevicePrincipalLiteral(p.TypesInfo, devVal, lit) {
				return
			}
			observed[rel] = struct{}{}
			if _, allowed := allowlist[rel]; allowed {
				return
			}
			pos := p.Fset.Position(lit.Pos())
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"DEVICE-PRINCIPAL-MINT-CALLER-01: auth.Principal{Kind: PrincipalDevice} is constructed in "+
						"%s outside the sole sanctioned device-principal issuer. A device principal must be minted "+
						"by runtime/auth.mintDevicePrincipal (deviceprincipal.go), which sets the unexported device "+
						"seal RowVisibility requires before deriving RowScopeDevice — a Principal{Kind: PrincipalDevice} "+
						"built elsewhere is type-inert (RowVisibility fails closed) and must not exist. Route through "+
						"mintDevicePrincipal; or, if this is a genuinely new sanctioned issuer, add it to "+
						"devicePrincipalMintAllowlist with a rationale.",
					rel,
				),
			})
		})
	}
	return diags, observed
}

// isDevicePrincipalLiteral reports whether lit is an auth.Principal composite
// literal whose Kind field is the compile-time constant auth.PrincipalDevice.
// Fail-open over a non-constant Kind (documented blind spot — inert without the
// device seal).
func isDevicePrincipalLiteral(info *types.Info, devVal constant.Value, lit *ast.CompositeLit) bool {
	if info == nil || devVal == nil || lit.Type == nil {
		return false
	}
	tv, ok := info.Types[lit.Type]
	if !ok || !isAuthPrincipalNamedType(tv.Type) {
		return false
	}
	kv, ok := FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
		id, isID := kv.Key.(*ast.Ident)
		return isID && id.Name == "Kind"
	})
	if !ok {
		return false // no Kind field set → zero value PrincipalUnknown, not a device mint
	}
	ktv, ok := info.Types[kv.Value]
	if !ok || ktv.Value == nil {
		return false // non-constant Kind — inert without the seal (blind spot)
	}
	return constant.Compare(ktv.Value, token.EQL, devVal)
}

// isAuthPrincipalNamedType reports whether t is the named type
// runtime/auth.Principal (alias-proof).
func isAuthPrincipalNamedType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == authRuntimePkgPath && obj.Name() == "Principal"
}

// authPrincipalDeviceValue resolves the compile-time constant value of
// auth.PrincipalDevice via the Pass's own package (when scanning runtime/auth)
// or its imported runtime/auth package (when scanning a package that imports it,
// e.g. the fixture), or nil if it cannot be resolved (treated as no-match).
func authPrincipalDeviceValue(p *Pass) constant.Value {
	if p.Pkg == nil {
		return nil
	}
	if p.Pkg.Path() == authRuntimePkgPath {
		if c, ok := p.Pkg.Scope().Lookup("PrincipalDevice").(*types.Const); ok {
			return c.Val()
		}
	}
	for _, imp := range p.Pkg.Imports() {
		if imp.Path() != authRuntimePkgPath {
			continue
		}
		if c, ok := imp.Scope().Lookup("PrincipalDevice").(*types.Const); ok {
			return c.Val()
		}
	}
	return nil
}
