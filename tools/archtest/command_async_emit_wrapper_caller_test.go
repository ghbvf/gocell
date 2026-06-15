//go:build archtest

// command_async_emit_wrapper_caller_test.go — locks the DOWNSTREAM (cell-side)
// caller-allowlist of the async command emit funnel: every call to a runtime
// command emit exit (command.EmitAsync / command.EmitAsyncFromIdempotencyKey)
// must originate from a generated per-command package (generated/contracts/
// command/**) or from runtime/command itself — never hand-written in a cell.
//
//   - INVARIANT: COMMAND-ASYNC-EMIT-CALLER-01
//
// This is the producer-side mirror of COMMAND-ASYNC-DISPATCH-CALLER-01 (which
// locks the consumer-side WithCommandDispatch map values to generated
// DispatchAsync symbols). Together with the upstream COMMAND-ASYNC-EMIT-FUNNEL-01
// (which locks bare kout.Emit/NewEntry command-topic construction to
// runtime/command) they form the producer funnel's nested double-lock:
//
//	cell business code
//	  → generated enqueue.EmitAsync (this rule: only generated may call ↓)
//	    → runtime command.EmitAsync (EMIT-FUNNEL-01: only it may call ↓)
//	      → kout.NewEntry
//
// ADR ref: docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
package archtest

import (
	"fmt"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

// commandEmitWrapperCallerFixturePkg is the RED fixture package path (loaded
// under the archtest_fixture tag for the negative-control self-check).
const commandEmitWrapperCallerFixturePkg = "./tools/archtest/internal/commandasyncemitcallerfixture"

// commandEmitExitCallee reports the runtime command emit-exit a call targets —
// EmitAsync or EmitAsyncFromIdempotencyKey in runtime/command — or ok=false for
// any other callee. Both exits are generic ([T any]); call.Fun is unwrapped from
// IndexExpr / IndexListExpr before resolution, mirroring
// commandTopicOfEmitOrNewEntry. Resolution is via ResolvePackageRef (go/types),
// so it is alias- and dot-import-proof and resolves same-package unqualified
// calls too.
//
// BOTH exits are locked deliberately: each takes a bare `dispatchID CommandID`,
// so leaving EmitAsyncFromIdempotencyKey open would let a cell emit with a bare
// DispatchID via the HTTP bridge — exactly the bare-string dispatch the generated
// per-command wrapper eliminates (#2059).
func commandEmitExitCallee(p *Pass, call *ast.CallExpr) (name string, ok bool) {
	fun := call.Fun
	if idx, isIdx := fun.(*ast.IndexExpr); isIdx {
		fun = idx.X
	} else if idxl, isIdxl := fun.(*ast.IndexListExpr); isIdxl {
		fun = idxl.X
	}
	pkgPath, n, resolved := ResolvePackageRef(p.TypesInfo, fun)
	if !resolved || pkgPath != commandEmitFunnelPkgPath {
		return "", false
	}
	if n == "EmitAsync" || n == "EmitAsyncFromIdempotencyKey" {
		return n, true
	}
	return "", false
}

// TestCommandAsyncEmitCaller01 asserts that every production call to a runtime
// command emit exit (command.EmitAsync / command.EmitAsyncFromIdempotencyKey)
// lives in a sanctioned package: a generated per-command package
// (generated/contracts/command/**, the typed EmitAsync wrapper #2059) or
// runtime/command itself (EmitAsyncFromIdempotencyKey delegates to EmitAsync).
// Any other caller hand-rolls async-command dispatch with a bare DispatchID,
// defeating the single-source key/payload guarantee the generated wrapper
// provides.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Upstream (wrapper existence): HARD — the generated EmitAsync wrapper is
//     codegen + golden-locked (command.tmpl render golden + `gocell generate
//     contract --all --verify` in CI). A wrapper that fails to bake DispatchID /
//     the typed *Request cannot be expressed through the generated path.
//   - Downstream (this archtest): MEDIUM — caller-allowlist type-aware scan, a
//     GO-LANGUAGE CEILING, not a deferred TODO. Go cannot express "only
//     generated/contracts/command/** may call this exported func". Same permanent
//     ceiling documented for COMMAND-ASYNC-EMIT-FUNNEL-01 /
//     COMMAND-ASYNC-DISPATCH-CALLER-01 / #851 / #893 / #1282. No fake Hard-upgrade
//     issue is opened.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. generated/contracts/command/** packages ARE sanctioned callers but are
//     excluded by Production() scope regardless — so the Production scan never
//     observes a sanctioned caller; the anti-vacuity anchor scans generated/
//     separately for the wrapper's existence.
//  2. Both exits have live production callers post-migration (#2059): EmitAsync
//     (device bootstrap + cert-renewal reconcile producers) and
//     EmitAsyncFromIdempotencyKey (the devicecmd HTTP EnqueueAsync bridge, #1610).
//     Both arms are therefore non-vacuous and both typed wrappers are generated —
//     neither is dead code. The anti-vacuity anchor below checks the EmitAsync
//     wrapper's existence, which is sufficient to prove the codegen funnel is wired.
//  3. Build-tag-gated production files under a non-default tag are not scanned by
//     the default-tags Production scan (same posture as the sibling funnels).
func TestCommandAsyncEmitCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// runtime/command OWNS the exits; its internal
		// EmitAsyncFromIdempotencyKey→EmitAsync delegation is sanctioned. Generated
		// per-command packages are the other sanctioned callers but are excluded
		// from Production() scope, so they never reach here.
		if p.Pkg.Path() == commandEmitFunnelPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				name, ok := commandEmitExitCallee(p, call)
				if !ok {
					return
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"COMMAND-ASYNC-EMIT-CALLER-01: %s calls runtime command.%s directly. "+
							"Async commands MUST be emitted through the generated per-command "+
							"EmitAsync wrapper (generated/contracts/command/**), which bakes in "+
							"DispatchID and locks the payload to the typed *Request — a cell never "+
							"names the raw routing topic. A direct command.%s call re-admits the "+
							"bare-DispatchID dispatch the generated wrapper eliminates (#2059; "+
							"symmetric to consumer COMMAND-ASYNC-DISPATCH-CALLER-01).",
						rel, name, name),
				})
			})
		}
		return d
	})

	// Anti-vacuity: at least one generated command package must declare an
	// EmitAsync wrapper that calls runtime command.EmitAsync, else the funnel
	// guards nothing — the Production scan above can never observe a sanctioned
	// caller because generated/ is excluded from Production() scope.
	if !observedGeneratedEmitWrapperPresent(t) {
		diags = append(diags, Diagnostic{
			Message: "COMMAND-ASYNC-EMIT-CALLER-01 anti-vacuity: no generated command package " +
				"declares an EmitAsync wrapper calling runtime command.EmitAsync. Either the " +
				"producer codegen (command.tmpl) was removed/renamed or the scanner regressed — " +
				"the wrapper funnel guards nothing without it.",
		})
	}

	Report(t, "COMMAND-ASYNC-EMIT-CALLER-01", diags)
}

// observedGeneratedEmitWrapperPresent verifies a generated command package
// declares an EmitAsync free function whose body calls runtime command.EmitAsync
// — the structural anti-vacuity anchor (the Production scan cannot see generated
// packages because they are excluded from Production() scope).
func observedGeneratedEmitWrapperPresent(t *testing.T) bool {
	t.Helper()
	var found bool
	_ = Run(t, Typed(TypedOpts{}, []string{"./generated/contracts/command/..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Recv != nil || fd.Name == nil || fd.Name.Name != "EmitAsync" {
					return
				}
				EachInSubtree[ast.CallExpr](fd, func(call *ast.CallExpr) {
					if name, ok := commandEmitExitCallee(p, call); ok && name == "EmitAsync" {
						found = true
					}
				})
			})
		}
		return nil
	})
	return found
}

// TestCommandAsyncEmitCaller01_RedFixture verifies the scanner fires against a
// hand-written package that calls command.EmitAsync AND
// command.EmitAsyncFromIdempotencyKey directly (bypassing the generated wrapper).
//
// The fixture must produce ≥ 2 diagnostics (one per exit). total==0 means the
// scanner is fail-open.
func TestCommandAsyncEmitCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{commandEmitWrapperCallerFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			// The fixture package path is neither runtime/command nor generated, so
			// every command-exit call there is a violation.
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					if _, ok := commandEmitExitCallee(p, call); ok {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 2,
		"COMMAND-ASYNC-EMIT-CALLER-01 RED fixture self-check FAILED: expected ≥ 2 "+
			"violations from commandasyncemitcallerfixture (direct command.EmitAsync + "+
			"command.EmitAsyncFromIdempotencyKey); got %d. found==0 means the scanner is "+
			"fail-open — check ResolvePackageRef resolves under the archtest_fixture tag.",
		found)
}
