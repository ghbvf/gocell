//go:build archtest

// INVARIANT: PHASE10-TEARCTX-PARENT-CHAIN-GUARD-01
//
// Locks two structural properties of the shutdown budget-isolation design in
// framework/runtime/bootstrap:
//
//  1. (Medium — funnel scan) phase10OrchestrateShutdown must NOT directly call
//     context.WithTimeout or context.WithDeadline. All budget-timeout
//     construction must be delegated to freshShutdownCtx. If phase10 were ever
//     changed to call context.WithTimeout directly (bypassing freshShutdownCtx),
//     the parent choice would no longer be enforced by a single point and could
//     silently drift to context.WithTimeout(drainCtx, ...), forming an
//     inheritance chain that bleeds the drain budget into the teardown stage.
//
//  2. (Hard — helper bake) freshShutdownCtx must always pass context.Background()
//     as the first argument to context.WithTimeout (or context.WithDeadline).
//     Parenting on any other ctx would break budget isolation: a blocked or
//     slow upstream stage could silently consume the downstream stage's budget.
//
// Together the two rules form a closed funnel:
//
//   - Upstream Medium: phase10 cannot bypass freshShutdownCtx to call
//     context.WithTimeout/WithDeadline directly.
//   - Downstream Hard: freshShutdownCtx cannot root on anything other than
//     context.Background().
//
// AI-robust rating:
//
//   - Rule ①: Medium — type-aware AST scan over phase10OrchestrateShutdown body.
//     The Go type system cannot prevent a direct context.WithTimeout call inside
//     a method body. Ceiling: same class as SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893) / MQTT-CONNECT-DEADLINE-DECOUPLED-01 —
//     all in-package callee scoping limits. No lower-cost Hard form exists.
//     This archtest itself is Medium (type-aware AST scan).
//   - Rule ②: the funnel API shape of freshShutdownCtx (no parent-ctx parameter)
//     makes it impossible for callers to pass a wrong parent — the wrong-parent
//     error cannot be expressed at the call site. This is Hard for callers.
//     However, the check that freshShutdownCtx itself passes context.Background()
//     as Args[0] to context.WithTimeout is a type-aware AST scan of the helper
//     body — that specific check is Medium (same class as rule ①). Together:
//     caller-side is Hard (API shape); helper-body compliance is Medium (this
//     archtest scan). A wrong parent inside freshShutdownCtx is caught here at
//     CI time even though it cannot be prevented at the Go type-system level.
//
// Anti-vacuity: both functions (phase10OrchestrateShutdown and freshShutdownCtx)
// are required to be found in the bootstrap package. If either is renamed or
// moved, the rule fails with an explicit "cannot find function, update rule"
// error rather than silently passing.
//
// Scanner blind spots:
//   - Function rename: if phase10OrchestrateShutdown or freshShutdownCtx is
//     renamed, anti-vacuity fires and forces a rule update.
//   - Inline refactor into an anonymous func or closure: EachInSubtree sees
//     CallExprs at any depth inside the FuncDecl body, so a nested call inside
//     a closure inside phase10 is still caught by rule ①.
//   - Additional callers of context.WithTimeout added elsewhere in the bootstrap
//     package are not covered by rule ① (it only scans phase10's body). This is
//     intentional scope: the invariant protects phase10's delegation pattern
//     specifically, not all WithTimeout usage in the package.
//
// ref: framework/runtime/bootstrap/phases_shutdown.go freshShutdownCtx +
//
//	phase10OrchestrateShutdown
//
// ref: docs/architecture/202605101730-adr-shutdown-budget-decouple.md
package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rulePhase10TearctxParentChainGuard = "PHASE10-TEARCTX-PARENT-CHAIN-GUARD-01"
	phase10BootstrapPkgPath            = PlatformFrameworkModulePath + "/runtime/bootstrap"
	phase10FixturePkg                  = PlatformModulePath + "/tools/archtest/internal/tearctxparentfixture"
)

// TestPhase10TearctxParentChainGuard01 runs both rule arms against the
// production bootstrap package and asserts zero violations.
//
// A single Run call collects anti-vacuity evidence (both functions found) and
// executes both rule assertions in the same packages.Load invocation, avoiding
// the overhead of a second cold load of the bootstrap package.
func TestPhase10TearctxParentChainGuard01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var phase10FD *ast.FuncDecl
	var helperFD *ast.FuncDecl
	var phase10DiagCount int
	var helperViolated bool

	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{phase10BootstrapPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != phase10BootstrapPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name == nil {
						return
					}
					// Anti-vacuity: collect both function declarations.
					switch fd.Name.Name {
					case "phase10OrchestrateShutdown":
						phase10FD = fd
					case "freshShutdownCtx":
						helperFD = fd
					}
					// Rule assertions require TypesInfo; only execute when available.
					if p.TypesInfo == nil || fd.Body == nil {
						return
					}
					switch fd.Name.Name {
					case "phase10OrchestrateShutdown":
						phase10DiagCount += countDirectWithTimeoutCalls(p, fd.Body)
					case "freshShutdownCtx":
						helperViolated = helperHasNonBackgroundParent(p, fd.Body)
					}
				})
			}
			return nil
		})

	// Anti-vacuity: both functions must be found.
	require.NotNil(t, phase10FD,
		"%s: cannot find phase10OrchestrateShutdown in %s — rule must be updated if function was renamed",
		rulePhase10TearctxParentChainGuard, phase10BootstrapPkgPath)
	require.NotNil(t, helperFD,
		"%s: cannot find freshShutdownCtx in %s — rule must be updated if function was renamed",
		rulePhase10TearctxParentChainGuard, phase10BootstrapPkgPath)

	// Rule ①: phase10 must not directly call context.WithTimeout/WithDeadline.
	assert.Equal(t, 0, phase10DiagCount,
		"%s rule①: phase10OrchestrateShutdown must not directly call context.WithTimeout "+
			"or context.WithDeadline (%d found); delegate to freshShutdownCtx",
		rulePhase10TearctxParentChainGuard, phase10DiagCount)

	// Rule ②: freshShutdownCtx must parent on context.Background().
	assert.False(t, helperViolated,
		"%s rule②: freshShutdownCtx must pass context.Background() as the first arg "+
			"to context.WithTimeout/WithDeadline",
		rulePhase10TearctxParentChainGuard)

	// No unexpected diagnostics from the scan.
	Report(t, rulePhase10TearctxParentChainGuard, diags)
}

// TestPhase10TearctxParentChainGuard01_RedFixture_Rule1_DirectWithTimeout verifies
// that rule ① correctly flags a phase10-like function that directly calls
// context.WithTimeout (the funnel bypass pattern).
func TestPhase10TearctxParentChainGuard01_RedFixture_Rule1_DirectWithTimeout(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const fixturePkg = phase10FixturePkg

	var directCallCount int

	_ = Run(t, Fixture(FixtureOpts{}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkg {
				return nil
			}
			for _, f := range p.Files {
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name == nil || fd.Name.Name != "violatingPhase" || fd.Body == nil {
						return
					}
					directCallCount += countDirectWithTimeoutCalls(p, fd.Body)
				})
			}
			return nil
		})

	assert.Greater(t, directCallCount, 0,
		"%s rule① RED fixture: scanner must detect direct context.WithTimeout in violatingPhase "+
			"— scanner is broken if this passes",
		rulePhase10TearctxParentChainGuard)
}

// TestPhase10TearctxParentChainGuard01_RedFixture_Rule2_NonBackgroundParent verifies
// that rule ② correctly flags a freshShutdownCtx-like helper that parents on a
// non-Background context (the budget-chain collapse pattern).
func TestPhase10TearctxParentChainGuard01_RedFixture_Rule2_NonBackgroundParent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const fixturePkg = phase10FixturePkg

	var violated bool

	_ = Run(t, Fixture(FixtureOpts{}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkg {
				return nil
			}
			for _, f := range p.Files {
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name == nil || fd.Name.Name != "violatingHelper" || fd.Body == nil {
						return
					}
					violated = helperHasNonBackgroundParent(p, fd.Body)
				})
			}
			return nil
		})

	assert.True(t, violated,
		"%s rule② RED fixture: scanner must detect non-Background parent in violatingHelper "+
			"— scanner is broken if this passes",
		rulePhase10TearctxParentChainGuard)
}

// TestPhase10TearctxParentChainGuard01_GreenFixture verifies that the compliant
// pattern (compliantPhase delegates to compliantHelper, which uses Background)
// produces no violations.
func TestPhase10TearctxParentChainGuard01_GreenFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const fixturePkg = phase10FixturePkg

	var phaseViolations int
	var helperViolated bool

	_ = Run(t, Fixture(FixtureOpts{}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkg {
				return nil
			}
			for _, f := range p.Files {
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name == nil || fd.Body == nil {
						return
					}
					switch fd.Name.Name {
					case "compliantPhase":
						phaseViolations += countDirectWithTimeoutCalls(p, fd.Body)
					case "compliantHelper":
						if helperHasNonBackgroundParent(p, fd.Body) {
							helperViolated = true
						}
					}
				})
			}
			return nil
		})

	assert.Equal(t, 0, phaseViolations,
		"%s GREEN fixture: compliantPhase must not be flagged by rule ①",
		rulePhase10TearctxParentChainGuard)
	assert.False(t, helperViolated,
		"%s GREEN fixture: compliantHelper must not be flagged by rule ②",
		rulePhase10TearctxParentChainGuard)
}

// countDirectWithTimeoutCalls counts context.WithTimeout or context.WithDeadline
// calls found directly in body (at any depth). Used for rule ①.
func countDirectWithTimeoutCalls(p *Pass, body *ast.BlockStmt) int {
	count := 0
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != "context" {
			return
		}
		if name == "WithTimeout" || name == "WithDeadline" {
			count++
		}
	})
	return count
}

// helperHasNonBackgroundParent reports whether body contains a
// context.WithTimeout or context.WithDeadline call whose first argument is NOT
// a call to context.Background() (or context.TODO()). Used for rule ②.
//
// A compliant helper always passes context.Background() as Args[0].
func helperHasNonBackgroundParent(p *Pass, body *ast.BlockStmt) bool {
	violated := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != "context" {
			return
		}
		if name != "WithTimeout" && name != "WithDeadline" {
			return
		}
		if len(call.Args) == 0 {
			// Cannot determine parent — treat as violation (fail-closed).
			violated = true
			return
		}
		arg0, isCall := call.Args[0].(*ast.CallExpr)
		if !isCall {
			// Args[0] is not a call expression (e.g. it's an ident) — violation.
			violated = true
			return
		}
		argPkg, argName, argOK := ResolvePackageRef(p.TypesInfo, arg0.Fun)
		if !argOK || argPkg != "context" || (argName != "Background" && argName != "TODO") {
			violated = true
		}
	})
	return violated
}
