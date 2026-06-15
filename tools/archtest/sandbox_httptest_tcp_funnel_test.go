//go:build archtest

// INVARIANT: SANDBOX-HTTPTEST-TCP-FUNNEL-01
//
// adapters/websocket and adapters/oidc MUST NOT call httptest.NewServer,
// httptest.NewTLSServer, or httptest.NewUnstartedServer directly in any test
// file. All httptest server construction in those packages MUST route through
// the sanctioned funnel package:
//
//	github.com/ghbvf/gocell/framework/pkg/testutil/nettest
//
// # Why
//
// CI/agent sandboxes forbid net.Listen. Bare httptest.NewServer(h) calls
// net.Listen("tcp", "127.0.0.1:0") unconditionally — causing a panic or hang
// when the sandbox kernel policy denies the syscall. The nettest package probes
// TCP availability first and calls t.Skip when the sandbox cannot bind, giving
// tests a clean skip path instead of a crash.
//
// # AI-robust rating (ai-robust.md §"Funnel 双向锁评级")
//
//   - Upstream (caller funnel, this file): Medium — the scan resolves callee
//     packages via ResolvePackageRef (go/types), covering qualified, alias, and
//     dot-import forms. Any bare httptest.NewServer/NewTLSServer/NewUnstartedServer
//     call site inside the two scanned packages triggers a Diagnostic.
//     Go-language ceiling: we cannot make httptest.NewServer unexported, so a
//     type-system Hard upstream form does not exist.
//   - Downstream (nettest funnel, Hard): nettest.NewServer/NewTLSServer bake the
//     skipIfNoTCP probe unconditionally — a caller cannot bypass the skip.
//     The probe is inside an if-err branch so the unconditionalskip analyzer does
//     not flag the helper.
//
// # Scan scope
//
// Currently limited to adapters/websocket and adapters/oidc (the known sandbox
// panic vectors). Expanding to repo-wide coverage is a follow-up (open a GitHub
// issue when other packages are found to call httptest.NewServer directly).
//
// # Blind spots (ai-robust.md §"工具选定后强制盲区自检")
//
//  1. Alias / dot-import: ResolvePackageRef covers both; tested via go/types
//     in typeseval's own suite.
//  2. httptest.NewUnstartedServer: banned as well, because once caller invokes
//     server.Start() it hits net.Listen. Listed alongside NewServer/NewTLSServer.
//  3. Files gated by non-default build tags (e.g., //go:build integration) would
//     be missed by a default-tag Typed scan. We use FlatNonDefaultTags() to
//     cover integration-tagged files (confirmed: adapters/websocket/
//     integration_test.go is gated by //go:build integration).
//  4. Packages outside adapters/websocket and adapters/oidc are not in scope;
//     follow-up issue tracks widening.
//
// # Reverse self-check (RED/GREEN fixture)
//
// TestSandboxHTTPTestTCPFunnel01_Fixture loads
// tools/archtest/internal/sandboxhttptestfixture/ via Run(t, Fixture(...)) with
// FixtureOpts{Tests: true} (the fixture source is in a _test.go file, mirroring
// the production _test.go-only filter) and asserts that:
//   - badBareServer (contains httptest.NewServer) is reported — RED must fire.
//   - goodNettestServer (uses nettest.NewServer) is NOT reported — GREEN must pass.
//
// Both the production scan and the fixture scan use the same scanBareHTTPTest
// predicate (single-source detection logic).
//
// Bypassing the self-check requires editing the real fixture source.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httptestPkgPath is the standard library package that owns NewServer /
// NewTLSServer / NewUnstartedServer.
const httptestPkgPath = "net/http/httptest"

// nettestPkgPath is the sanctioned funnel package. Call sites in this package
// are allowed to reference httptest directly (it IS the funnel wrapper).
const nettestPkgPath = PlatformFrameworkModulePath + "/pkg/testutil/nettest"

// bannedHTTPTestFuncs is the closed set of httptest constructor names whose
// direct use in the scanned packages is forbidden.
var bannedHTTPTestFuncs = map[string]bool{
	"NewServer":          true,
	"NewTLSServer":       true,
	"NewUnstartedServer": true,
}

// sandboxHTTPTestScanPkgs is the closed set of packages scanned by
// SANDBOX-HTTPTEST-TCP-FUNNEL-01. scanPkgs (slice) is derived from this map so
// both data structures always agree on the package set.
var sandboxHTTPTestScanPkgs = map[string]bool{
	PlatformModulePath + "/adapters/websocket": true,
	PlatformModulePath + "/adapters/oidc":      true,
}

// scanPkgs returns the package paths for SANDBOX-HTTPTEST-TCP-FUNNEL-01 as a
// slice, derived from sandboxHTTPTestScanPkgs so there is a single source of
// truth for the scan set.
func sandboxHTTPTestScanPkgsList() []string {
	pkgs := make([]string, 0, len(sandboxHTTPTestScanPkgs))
	for pkg := range sandboxHTTPTestScanPkgs {
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

// scanBareHTTPTest is the single-source detection predicate for
// SANDBOX-HTTPTEST-TCP-FUNNEL-01. It scans all _test.go files in p for bare
// httptest.NewServer / NewTLSServer / NewUnstartedServer call sites and returns
// one Diagnostic per violation.
//
// Both the production test (TestSandboxHTTPTestTCPFunnel01) and the fixture
// test (TestSandboxHTTPTestTCPFunnel01_Fixture) call this function, ensuring
// the fixture mirrors the exact production predicate.
func scanBareHTTPTest(p *Pass) []Diagnostic {
	if !p.Typed() {
		return nil
	}
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if !strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok {
				return
			}
			if pkgPath == httptestPkgPath && bannedHTTPTestFuncs[name] {
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: fmt.Sprintf(
						"bare httptest.%s call — use nettest.%s(t, handler) from %s instead "+
							"(skips in sandboxes where net.Listen is forbidden; "+
							"return type stays *httptest.Server, keep the httptest import).",
						name, name, nettestPkgPath,
					),
				})
			}
		})
	}
	return d
}

// TestSandboxHTTPTestTCPFunnel01 enforces SANDBOX-HTTPTEST-TCP-FUNNEL-01.
//
// It scans test files (Tests: true) in adapters/websocket and adapters/oidc
// under FlatNonDefaultTags() (covers integration-tagged files) and the default
// (nil-tags) context, and reports any bare httptest.NewServer /
// NewTLSServer / NewUnstartedServer call site.
//
// Anti-vacuity: asserts at least one nettest.NewServer or nettest.NewTLSServer
// call is observed — proving the migration actually landed and the scan is not
// loading empty packages.
func TestSandboxHTTPTestTCPFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "SANDBOX-HTTPTEST-TCP-FUNNEL-01"

	scanPkgs := sandboxHTTPTestScanPkgsList()

	var allDiags []Diagnostic
	var nettestCallsObserved int

	// Run twice: once with integration + other non-default tags (covers
	// integration_test.go), once without (covers default-build test files).
	// FlatNonDefaultTags() includes the "integration" tag.
	for _, tags := range [][]string{FlatNonDefaultTags(), nil} {
		opts := TypedOpts{Tests: true, Tags: tags}
		diags := Run(t, Typed(opts, scanPkgs), func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			if !sandboxHTTPTestScanPkgs[p.Pkg.Path()] {
				return nil
			}
			// Count nettest funnel calls for anti-vacuity (separate from the
			// shared predicate which only reports violations).
			for _, file := range p.Files {
				if !strings.HasSuffix(p.Rel(file), "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok {
						return
					}
					if pkgPath == nettestPkgPath &&
						(name == "NewServer" || name == "NewTLSServer") {
						nettestCallsObserved++
					}
				})
			}
			return scanBareHTTPTest(p)
		})
		allDiags = append(allDiags, diags...)
	}

	Report(t, ruleID, allDiags)

	// Anti-vacuity: the migration must have landed at least one nettest call.
	// If this is zero the scanner is probably not loading the test files, or the
	// migration was rolled back.
	assert.Greater(t, nettestCallsObserved, 0,
		"%s anti-vacuity: observed 0 nettest.NewServer/NewTLSServer calls in "+
			"adapters/websocket and adapters/oidc — either the migration was "+
			"rolled back or the scanner failed to load test files. "+
			"Check TypedOpts{Tests: true} and that the packages are reachable.",
		ruleID)
}

// TestSandboxHTTPTestTCPFunnel01_Fixture loads the RED/GREEN fixture package
// and asserts:
//   - badBareServer (httptest.NewServer) → exactly 1 Diagnostic (RED fires).
//   - goodNettestServer (nettest.NewServer) → 0 extra Diagnostics (GREEN passes).
//
// The fixture file is a _test.go file (fixture_shim_test.go) and is loaded
// with FixtureOpts{Tests: true}, so the fixture mirrors the production rule's
// _test.go-only filter exactly (single-source predicate via scanBareHTTPTest).
func TestSandboxHTTPTestTCPFunnel01_Fixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "SANDBOX-HTTPTEST-TCP-FUNNEL-01"
	const fixturePkg = "./tools/archtest/internal/sandboxhttptestfixture"

	diags := Run(t, Fixture(FixtureOpts{Tests: true}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			return scanBareHTTPTest(p)
		})

	// RED: exactly one violation must be found (badBareServer's httptest.NewServer).
	require.Len(t, diags, 1,
		"%s fixture RED check: expected exactly 1 bare httptest.NewServer diagnostic "+
			"(from badBareServer); got %d. Either the planted violation was removed or "+
			"the scanner is not resolving httptest correctly.",
		ruleID, len(diags))

	assert.Contains(t, diags[0].Message, "httptest.NewServer",
		"%s fixture RED check: diagnostic should name httptest.NewServer", ruleID)
}
