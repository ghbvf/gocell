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
	"strings"
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
// Label convention matches the ADR §4 matrix row (上游 Medium / 下游 Hard) and the
// sibling CALLER-family godocs (EMIT-FUNNEL / DISPATCH-CALLER): Upstream = this
// caller-allowlist archtest; Downstream = the structural backstop.
//
//   - Upstream (caller-allowlist, this archtest): MEDIUM — type-aware scan, a
//     GO-LANGUAGE CEILING, not a deferred TODO. Go cannot express "only
//     generated/contracts/command/** may call this exported func". Same permanent
//     ceiling documented for COMMAND-ASYNC-EMIT-FUNNEL-01 /
//     COMMAND-ASYNC-DISPATCH-CALLER-01 / #851 / #893 / #1282. No fake Hard-upgrade
//     issue is opened.
//   - Downstream (wrapper existence, codegen + golden): HARD — the generated
//     EmitAsync / EmitAsyncFromIdempotencyKey wrappers are codegen + golden-locked
//     (command.tmpl render golden + `gocell generate contract --all --verify` in
//     CI). A wrapper that fails to bake DispatchID / the typed *Request cannot be
//     expressed through the generated path (symmetric to the Hard half of
//     COMMAND-GEN-FUNNEL-SOLE-EMITTER-01).
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. generated/ is excluded from Production() scope, so the Production scan never
//     observes a generated caller. Generated packages are handled by two separate
//     typed scans over generated/: (a) the anti-vacuity anchor verifies the
//     command wrappers exist (both arms); (b) nonCommandGeneratedEmitCallers flags
//     any NON-command generated package calling a runtime emit exit (#2059 F2) —
//     so the stated allowlist {generated/contracts/command/**, runtime/command} is
//     actually enforced, not assumed. (b) is vacuous-green today (no such caller).
//  2. Both exits have live production callers post-migration (#2059): EmitAsync
//     (device bootstrap + cert-renewal reconcile producers) and
//     EmitAsyncFromIdempotencyKey (the devicecmd HTTP EnqueueAsync bridge, #1610).
//     Both arms are therefore non-vacuous and both typed wrappers are generated —
//     neither is dead code. The anti-vacuity anchor below checks BOTH wrappers'
//     existence so a template regression dropping either arm fails closed.
//  3. _test.go files — including build-tag-gated tests such as `integration` — are
//     EXCLUDED from the Production() scan (TypedOpts.Tests defaults false). A direct
//     command.EmitAsync / EmitAsyncFromIdempotencyKey call in a test helper or
//     integration test is therefore NOT caught; such calls should still be migrated
//     to the generated wrapper for production↔test funnel consistency (the #2059
//     migration covers the iotdevice durable integration test for this reason).
//  4. Build-tag-gated production files under a non-default tag are not scanned by
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

	// Anti-vacuity: a generated command package must declare BOTH wrappers
	// (EmitAsync + EmitAsyncFromIdempotencyKey), each calling its runtime exit,
	// else the funnel guards nothing — the Production scan above can never observe
	// a sanctioned caller because generated/ is excluded from Production() scope.
	// Both arms are checked so a template regression dropping either wrapper (whose
	// exit this rule also locks) fails closed, not just the EmitAsync arm.
	if missing := missingGeneratedEmitWrappers(t); len(missing) > 0 {
		diags = append(diags, Diagnostic{
			Message: "COMMAND-ASYNC-EMIT-CALLER-01 anti-vacuity: no generated command package " +
				"declares a wrapper calling runtime command." + strings.Join(missing, " / command.") +
				". Either the producer codegen (command.tmpl) was removed/renamed or the scanner " +
				"regressed — the wrapper funnel guards nothing without it.",
		})
	}

	// Generated-scope enforcement (#2059 F2): the stated allowlist is
	// {generated/contracts/command/**, runtime/command}. The Production scan above
	// excludes ALL generated/, so a NON-command generated package directly calling a
	// runtime emit exit would slip through. Scan generated/contracts/** and flag any
	// emit-exit caller whose package is not under generated/contracts/command/, so the
	// enforcement matches the allowlist the rule claims (not just command anti-vacuity).
	diags = append(diags, nonCommandGeneratedEmitCallers(t)...)

	Report(t, "COMMAND-ASYNC-EMIT-CALLER-01", diags)
}

// missingGeneratedEmitWrappers returns the runtime emit-exit names for which NO
// generated command package declares a same-named wrapper free function whose
// body calls that runtime exit — the structural anti-vacuity anchor (the
// Production scan cannot see generated packages because they are excluded from
// Production() scope). Both exits are checked so a template regression dropping
// either wrapper is caught; an empty result means both wrappers are wired.
func missingGeneratedEmitWrappers(t *testing.T) []string {
	t.Helper()
	expected := []string{"EmitAsync", "EmitAsyncFromIdempotencyKey"}
	found := map[string]bool{}
	_ = Run(t, Typed(TypedOpts{}, []string{"./generated/contracts/command/..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Recv != nil || fd.Name == nil {
					return
				}
				wrapperName := fd.Name.Name
				if wrapperName != "EmitAsync" && wrapperName != "EmitAsyncFromIdempotencyKey" {
					return
				}
				// The wrapper must delegate to the SAME-named runtime exit.
				EachInSubtree[ast.CallExpr](fd, func(call *ast.CallExpr) {
					if name, ok := commandEmitExitCallee(p, call); ok && name == wrapperName {
						found[wrapperName] = true
					}
				})
			})
		}
		return nil
	})
	var missing []string
	for _, name := range expected {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// generatedCommandPkgInfix is the import-path infix marking the SOLE sanctioned
// generated caller of the runtime emit exits — the per-command generated wrappers
// under generated/contracts/command/**. Any OTHER generated/contracts/** package
// calling a runtime emit exit is a funnel bypass (#2059 F2).
const generatedCommandPkgInfix = "/generated/contracts/command/"

// nonCommandGeneratedEmitCallers scans generated/contracts/** and returns a
// diagnostic for every direct runtime emit-exit call (command.EmitAsync /
// command.EmitAsyncFromIdempotencyKey) made from a generated package NOT under
// generated/contracts/command/. The Production scan in the main test excludes all
// of generated/ by scope; this closes that hole so the rule actually enforces its
// stated allowlist for generated packages, not only the command anti-vacuity.
// Reuses the production-proven commandEmitExitCallee detector (RED-fixture covered),
// so only the package-prefix gate is new; today this is vacuous-green (no
// non-command generated package calls the exits).
func nonCommandGeneratedEmitCallers(t *testing.T) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{"./generated/contracts/..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		if strings.Contains(p.Pkg.Path(), generatedCommandPkgInfix) {
			return nil // sanctioned generated caller
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				name, ok := commandEmitExitCallee(p, call)
				if !ok {
					return
				}
				pos := p.Fset.Position(call.Pos())
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"COMMAND-ASYNC-EMIT-CALLER-01: %s is a NON-command generated package "+
							"calling runtime command.%s directly. Only generated/contracts/command/** "+
							"per-command wrappers may call the runtime emit exits (#2059 F2).",
						rel, name),
				})
			})
		}
		return nil
	})
	return diags
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
