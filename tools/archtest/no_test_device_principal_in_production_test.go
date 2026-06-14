//go:build archtest

// INVARIANT: NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01
//
// # NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01
//
// Invariant: auth.MustNewTestDevicePrincipal(...) must only appear in _test.go
// files. It is an EXPORTED helper that delegates to the unexported
// mintDevicePrincipal and therefore produces a fully sealed PrincipalDevice
// WITHOUT going through JWT verification. Calling it in production code would
// forge a verified device identity and bypass the device-token authn boundary.
//
// Why this guard is the closure for DEVICE-PRINCIPAL-MINT-CALLER-01: the seal is
// Hard (the unexported deviceSeal type + Principal.device field cannot be set
// from outside runtime/auth), so package-external code cannot FABRICATE a seal by
// struct literal. But the test helper is exported precisely so external test
// packages (corecells, tests/integration) can build a sealed device principal
// without the full Issue→AuthenticateBearer chain — which means external code
// CAN obtain a seal by CALLING it. This rule pins that exported escape hatch to
// non-production code, so the only production path to a sealed device principal
// remains mintDevicePrincipal via the verified bearer path. Same posture +
// permanent Go-language ceiling as NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01
// (TestServiceContext bypasses the service-token HMAC guard identically); the
// helper cannot live outside package auth (it needs the unexported minter) nor
// in _test.go (external packages must see it), so a Medium archtest is the
// ceiling and the seal stays the Hard control. This guard is also the
// production-caller closure registered against the KERNEL-MUSTCTOR-PRODUCTION-DECL-01
// carve-out for runtime/auth.MustNewTestDevicePrincipal (#2115).
//
// AI-robust 评级：Medium-true (type-aware via Production(TypedOpts{Tests: false})
// + ResolvePackageRef). Resolution is by canonical *types.PkgName import path
// (github.com/ghbvf/gocell/framework/runtime/auth), NOT AST identifier name, so an
// aliased import (`import rauth ".../framework/runtime/auth";
// rauth.MustNewTestDevicePrincipal(...)`) or a dot-import cannot bypass detection.
// The prior AST-only `id.Name == "auth"` matcher silently passed those shapes
// (#2142 review F1); the deviceprincipalfixture alias-bypass red fixture pins the
// anti-vacuity contract. Mirrors AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01's
// type-aware upgrade. Function-value capture (`fn := auth.MustNewTestDevicePrincipal`)
// remains an out-of-scope theoretical blind spot, same as sibling caller-scan rules.
//
// Detection: Production(TypedOpts{Tests: false}) loads every non-test production
// package across the workspace; ResolvePackageRef matches calls resolving to the
// canonical auth.MustNewTestDevicePrincipal symbol. _test.go files are excluded
// in-scan (test helpers call it freely).
package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleNoTestDevicePrincipalInProduction = "NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01"

// authImportSuffix is the module-relative path of the runtime/auth package.
// Combined with modulePath (read from go.mod) it forms the canonical import path
// matched by ResolvePackageRef — alias-proof, since resolution is by *types.PkgName
// import path rather than syntactic identifier.
const authImportSuffix = "/framework/runtime/auth"

type deviceForgeHit struct {
	file string
	line int
}

// scanDeviceForgePass walks a single Pass and records non-test calls to
// auth.MustNewTestDevicePrincipal, resolved by canonical import path so that
// import aliases and dot-imports cannot evade detection.
func scanDeviceForgePass(p *Pass, modulePath string) []deviceForgeHit {
	if p.TypesInfo == nil || p.Pkg == nil {
		return nil
	}
	authImportPath := modulePath + authImportSuffix

	var hits []deviceForgeHit
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok || pkgPath != authImportPath || name != "MustNewTestDevicePrincipal" {
				return
			}
			hits = append(hits, deviceForgeHit{
				file: rel,
				line: p.Fset.Position(call.Pos()).Line,
			})
		})
	}
	return hits
}

// TestNO_TEST_DEVICE_PRINCIPAL_IN_PRODUCTION_01 enforces that
// auth.MustNewTestDevicePrincipal is never called from non-test production code.
func TestNO_TEST_DEVICE_PRINCIPAL_IN_PRODUCTION_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)

	var hits []deviceForgeHit
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		hits = append(hits, scanDeviceForgePass(p, modulePath)...)
		return nil
	})

	for _, h := range hits {
		t.Logf("%s: %s:%d calls auth.MustNewTestDevicePrincipal in non-test production code — move to _test.go",
			ruleNoTestDevicePrincipalInProduction, h.file, h.line)
	}
	assert.Empty(t, hits,
		ruleNoTestDevicePrincipalInProduction+": auth.MustNewTestDevicePrincipal forges a sealed "+
			"device principal without JWT verification; it must only be called from _test.go files")
}

// TestNoTestDevicePrincipal_ScannerCatchesAliasBypass loads the build-tag-gated
// deviceprincipalfixture package and asserts the type-aware scanner reports the
// aliased-import call site that the prior AST-only `id.Name == "auth"` matcher
// silently passed (#2142 review F1).
//
// ResolvePackageRef resolves info.Uses[sel.X] → *types.PkgName → Imported().Path(),
// so the canonical import path matches regardless of the alias chosen at import.
//
// Per ai-robust.md §"Hard 范本": the fixture is a real Go package loaded via
// packages.Load with the archtest_fixture build tag. Bypassing this test requires
// modifying real source code.
func TestNoTestDevicePrincipal_ScannerCatchesAliasBypass(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)

	var hits []deviceForgeHit
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/deviceprincipalfixture"}),
		func(p *Pass) []Diagnostic {
			hits = append(hits, scanDeviceForgePass(p, modulePath)...)
			return nil
		})

	require.Len(t, hits, 1,
		"fixture must yield exactly 1 violation: AliasedForge uses "+
			"`import rauth \"<module>/framework/runtime/auth\"; rauth.MustNewTestDevicePrincipal(...)`; "+
			"the prior AST-only id.Name == \"auth\" matcher would silently pass this")
	assert.Contains(t, hits[0].file, "tools/archtest/internal/deviceprincipalfixture/",
		"fixture hit must be located in the deviceprincipalfixture package directory")
}
