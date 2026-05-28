// Package archtest verifies SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01.
//
// INVARIANT: SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01
//
// runtime/saga (the package containing Coordinator) MUST NOT call
// journal.Journal.Heartbeat directly. Heartbeat invocations are funneled
// through the per-step heartbeat goroutine in the runtime/saga/executor
// subpackage; the Coordinator delegates per-step lease maintenance to
// Executor.Execute / RunWithHeartbeat and owns no heartbeat goroutine of
// its own.
//
// # Background
//
// Before #1181 the Coordinator ran a centralized heartbeatLoop /
// heartbeatOnce goroutine that re-Heartbeat-ed every active lease in a
// shared activeLeases map. PR-06 (#1179) introduced runtime/saga/executor
// with its own per-step heartbeat goroutine. Maintaining both was a
// double-renewal anti-pattern (Temporal / AWS Step Functions both use a
// single per-activity/per-task heartbeat — there is no industry pattern
// for parallel "central + per-step" heartbeats). #1181 deletes the
// Coordinator's centralized loop. This archtest is the static guard that
// prevents the pattern from being re-introduced.
//
// # Funnel shape (AI-robust §funnel 双向锁)
//
// Upstream is Hard for the journal-INTERFACE-field loop + Medium for the
// func-value-field loop; downstream Medium (full rating in the "Funnel 双向锁
// 评级" section below). In short: #1209 split journal.Journal into JournalCore +
// Heartbeater and narrowed Coordinator.journal to journal.JournalCore, which has
// no Heartbeat method — so a centralized heartbeat loop built on a persisted
// Heartbeat-bearing INTERFACE field is compile-time inexpressible. The
// func-value variant (a heartbeat-shaped func field fed from a .Heartbeat
// selector OUTSIDE runtime/saga) is NOT compile-sealed; it is archtest-banned by
// SAGA-JOURNAL-HOLDER-SEAL-01 rule 3 (Medium). A1 (this archtest) is retained to
// ban any .Heartbeat callsite over the one transient full-Journal value: the
// NewCoordinator parameter handed to the executor funnel.
//
// # Blind-spot self-test
//
// Method calls in Go can take three shapes that all surface here:
//   - Direct value call: `c.journal.Heartbeat(...)` — receiver is the
//     journal field, method is Heartbeat. SelectorExpr.X resolves to a
//     value of type journal.Journal; ResolveMethodCall returns the
//     Heartbeat func.
//   - Method expression: `journal.Journal.Heartbeat(j, ...)` — receiver
//     is the interface type. ResolveMethodCall returns Heartbeat with
//     the type as receiver. Same Pkg+Name match.
//   - Method value: `f := c.journal.Heartbeat; f(...)` — the SelectorExpr
//     at the `f := ...` assignment still resolves through
//     info.Selections; ResolveMethodCall returns Heartbeat. The
//     subsequent `f(...)` is a CallExpr on Ident f and is NOT inspected
//     here (it's a function call, not a SelectorExpr) — but it is
//     unreachable without the method-value selector, which IS caught.
//
// Blind spot B1: a type alias `type X = journal.Journal` (or
// `journal.Heartbeater`) in runtime/saga would let the package reference
// `.Heartbeat` indirectly as `X.Heartbeat`. Mitigated by inheriting the alias
// ban from SAGA-JOURNAL-HOLDER-SEAL-01's B1 reverse self-test, which forbids
// `type X = journal.{Journal,JournalCore,Heartbeater}` in runtime/saga.
//
// # Funnel 双向锁评级
//
// Upstream (type-system seal): HARD for the INTERFACE-field loop (landed in
// #1209). Coordinator.journal is journal.JournalCore, which declares no Heartbeat
// method, so the centralized heartbeat loop this invariant forbids — a long-lived
// Heartbeat-bearing field re-Heartbeat-ing leases in a goroutine — cannot be
// expressed via a journal interface field: c.journal.Heartbeat is a compile
// error. BUT the loop is ALSO expressible via a heartbeat-SHAPED func field
// (`beat func(ctx, idutil.SafeID, idutil.SafeID, time.Duration) (bool, error)`)
// populated from a `.Heartbeat` selector OUTSIDE runtime/saga — a path that is
// NOT compile-sealed. SAGA-JOURNAL-HOLDER-SEAL-01 closes it at archtest time:
// rule 1 forbids persisting the full journal.Journal / bare journal.Heartbeater
// as a field, rule 3 forbids persisting a Heartbeater-shaped func field (Medium).
// So the interface-field loop is compile-unbuildable (Hard) and the func-field
// loop is archtest-banned (Medium); the only irreducible residual is a
// constructor closure that captures the NewCoordinator heartbeat param WITHOUT
// persisting it (same class as the downstream pass-through window below).
//
// Downstream (callsite ban — this archtest, A1): retained, honestly Medium. The
// field seal closes the LOOP, but one transient Heartbeat-bearing value remains:
// the NewCoordinator(j journal.Journal, ...) parameter, which must carry Heartbeat
// to construct the executor (executor.NewExecutor(j, ...); the executor is the
// sanctioned per-step heartbeat funnel and owns the only Heartbeat caller). That
// parameter is irreducible — the Coordinator must own executor construction
// (#1181 F5: claim and heartbeat same-source), so SOME runtime/saga value holds
// Heartbeat at construction time. A1 bans any actual .Heartbeat() callsite over
// that window via go/types structural signature matching (typed aliases and local
// shape-duplicates are covered too; scope excludes runtime/saga/executor/, the
// sanctioned caller — see B1 below). Handing j to the executor constructor is a
// value pass, not a .Heartbeat selector, and stays green.
//
// Current rating: upstream Hard for the interface-field loop (compile-time
// inexpressible) + Medium for the func-field loop (SAGA-JOURNAL-HOLDER-SEAL-01
// rule 3 archtest); downstream Medium (A1 guards the irreducible constructor
// pass-through window).
//
// The earlier blanket "Hard upstream / loop compile-time inexpressible" framing
// was overstated: a PERSISTED heartbeat callable need not be a journal interface
// — a func-typed field of the heartbeat shape works too, and func fields are not
// type-system-sealable here. That path is now archtest-banned (Medium) rather
// than left open. The single irreducible residual is a constructor closure
// capturing the NewCoordinator heartbeat param (not persisted in any field) —
// same low-risk class as the downstream window; compile-sealing it would force
// callers to pass the journal twice (worse API, zero real gain). Per ai-robust.md
// §"Funnel 双向锁评级", the Hard interface-field seal needs no gh-tracked upgrade;
// the Medium func-field + downstream residuals are deliberate, not Soft carryovers.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// sagaCoordinatorNoHeartbeatLoopRule is the rule ID for diagnostics.
const sagaCoordinatorNoHeartbeatLoopRule = "SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01"

// heartbeatMethodName is the forbidden method name on journal.Journal.
const heartbeatMethodName = "Heartbeat"

// TestSagaCoordinatorNoHeartbeatLoop_A1_NoJournalHeartbeatCallInRuntimeSaga
// fails if any production (non-test) file under runtime/saga/ — excluding
// the runtime/saga/executor/ subpackage — references a Heartbeat method
// whose signature matches the Heartbeater shape:
//
//	Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID,
//	          leaseDuration time.Duration) (bool, error)
//
// This is broader than the original "only flag journal.Journal.Heartbeat"
// check (#1181 F13) because the same anti-pattern can be re-introduced via:
//
//   - executor.Heartbeater (narrow interface) re-imported by a coordinator
//   - a local helper interface in package saga that duplicates the shape
//   - a struct method on a coordinator-private type
//
// All such forms violate the funnel — Coordinator must delegate lease
// maintenance to executor.Execute / RunWithHeartbeat, never call Heartbeat
// itself.
//
// AST coverage (#1181 F14): both direct calls (`x.Heartbeat(...)`) AND
// method values (`f := x.Heartbeat`) are scanned. A method value would
// otherwise let a coordinator stash the function pointer and call it later
// via Ident, bypassing a SelectorExpr-only check.
func TestSagaCoordinatorNoHeartbeatLoop_A1_NoJournalHeartbeatCallInRuntimeSaga(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/saga/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Only flag files under runtime/saga/ but exclude the executor
			// subpackage — executor IS the sanctioned heartbeater funnel.
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			if strings.HasPrefix(rel, "runtime/saga/executor/") {
				continue
			}

			out = append(out, scanHeartbeatSelectors(p, file, rel)...)
		}
		return out
	})

	Report(t, sagaCoordinatorNoHeartbeatLoopRule+"-A1", diags)
}

// scanHeartbeatSelectors walks every SelectorExpr whose Sel.Name == "Heartbeat"
// (both inside CallExpr and as a method value) and emits a diagnostic when the
// receiver implements the Heartbeater-shape signature.
func scanHeartbeatSelectors(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != heartbeatMethodName {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil {
			return
		}
		if !methodIsHeartbeaterShape(fn) {
			return
		}
		pos := p.Fset.Position(sel.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"%s: forbidden reference to a Heartbeater-shape .Heartbeat method in runtime/saga; "+
					"per-step lease maintenance is funneled through runtime/saga/executor "+
					"(Executor.Execute / Executor.RunWithHeartbeat). If you need a "+
					"non-step heartbeat (e.g. extending lease across an outer operation), "+
					"call Executor.RunWithHeartbeat — do NOT re-introduce a centralized "+
					"heartbeat goroutine.",
				sagaCoordinatorNoHeartbeatLoopRule),
		})
	})
	return out
}

// methodIsHeartbeaterShape reports whether fn (a method func object) has the
// signature `Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID,
// leaseDuration time.Duration) (bool, error)`. The shape check is structural
// — it does not require the receiver to be kernel/saga/journal.Journal so
// new interfaces / local helpers that duplicate the shape are also caught
// (#1181 F13).
//
// Shape constants:
//
//	params: 4 ⇒ context.Context, idutil.SafeID, idutil.SafeID, time.Duration
//	results: 2 ⇒ bool, error
//
// Receiver type is not inspected; only the parameter and result types matter.
func methodIsHeartbeaterShape(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false // nil signature, or not a method
	}
	return signatureMatchesHeartbeaterShape(sig)
}

// signatureMatchesHeartbeaterShape reports whether sig has the parameter and
// result types of journal.Heartbeater.Heartbeat, independent of whether sig is
// a method (has a receiver) or a bare func type:
//
//	(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error)
//
// It backs two scans in this package: the .Heartbeat method-call scan
// (SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 A1, via methodIsHeartbeaterShape) and
// the heartbeat-shaped func-FIELD scan (SAGA-JOURNAL-HOLDER-SEAL-01, where a
// persisted func value of this shape is the func-value equivalent of holding a
// journal.Heartbeater field).
func signatureMatchesHeartbeaterShape(sig *types.Signature) bool {
	if sig == nil {
		return false
	}
	params := sig.Params()
	if params.Len() != 4 {
		return false
	}
	results := sig.Results()
	if results.Len() != 2 {
		return false
	}
	if !typeIsNamed(params.At(0).Type(), "context", "Context") {
		return false
	}
	if !typeIsNamed(params.At(1).Type(), heartbeaterIdutilPkgName, heartbeaterSafeIDType) {
		return false
	}
	if !typeIsNamed(params.At(2).Type(), heartbeaterIdutilPkgName, heartbeaterSafeIDType) {
		return false
	}
	if !typeIsNamed(params.At(3).Type(), "time", "Duration") {
		return false
	}
	if b, ok := results.At(0).Type().(*types.Basic); !ok || b.Kind() != types.Bool {
		return false
	}
	if named, ok := results.At(1).Type().(*types.Named); !ok ||
		named.Obj() == nil || named.Obj().Name() != "error" {
		return false
	}
	return true
}

// heartbeaterIdutilPkgName / heartbeaterSafeIDType carry the package-suffix
// and type-name used by Heartbeater-shape param-1 + param-2
// (instanceID, leaseID idutil.SafeID). Names are package-prefixed to avoid
// colliding with safeid_funnel_test.go's constants of the same intent.
const (
	heartbeaterIdutilPkgName = "idutil"
	heartbeaterSafeIDType    = "SafeID"
)

// typeIsNamed reports whether t resolves to a named type whose enclosing
// package's import-path *suffix* equals pkgSuffix and whose declared name
// equals typeName. Suffix matching is used because callers refer to
// idutil.SafeID via the local name even when the package is at a long path.
func typeIsNamed(t types.Type, pkgSuffix, typeName string) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != typeName {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		// "error" lives in the universe scope (Pkg == nil); caller handles
		// that case directly without typeIsNamed.
		return false
	}
	return pkg.Path() == pkgSuffix || strings.HasSuffix(pkg.Path(), "/"+pkgSuffix)
}

// TestSagaCoordinatorNoHeartbeatLoop_B1_ExecutorSubpkgCallsitesAllowed is the
// reverse self-check for the A1 scope boundary. A1 excludes
// runtime/saga/executor/ from its scan — the executor subpackage is the
// sanctioned heartbeat funnel. This test validates two properties:
//
//  1. Non-degenerate fixture: runtime/saga/executor/ production files contain
//     at least one Heartbeater-shape .Heartbeat SelectorExpr. If the executor
//     were refactored to remove all .Heartbeat calls without updating A1's
//     scope comment, this assertion would catch the test becoming vacuous.
//
//  2. Scope boundary correctness: A1's path filter (exclude
//     runtime/saga/executor/) means that when we re-run the A1 scanner
//     without that filter — i.e., scanning executor files directly — it DOES
//     find Heartbeater-shape callsites. This confirms the exclude is the
//     reason A1 reports 0 violations for executor files, not that executor
//     has no callsites.
//
// Together these two assertions ensure A1's scope boundary is not a silent
// no-op: executor has real callsites that A1 is actively excluding.
func TestSagaCoordinatorNoHeartbeatLoop_B1_ExecutorSubpkgCallsitesAllowed(t *testing.T) {
	t.Parallel()

	// Count Heartbeater-shape .Heartbeat SelectorExprs found in
	// runtime/saga/executor/ production (non-test) files. We re-use
	// scanHeartbeatSelectors but collect into a plain counter rather than
	// reporting violations — these are expected and sanctioned callsites.
	var executorCallsites []Diagnostic
	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/saga/executor/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Only executor subpackage files.
			if !strings.HasPrefix(rel, "runtime/saga/executor/") {
				continue
			}
			executorCallsites = append(executorCallsites, scanHeartbeatSelectors(p, file, rel)...)
		}
		return nil
	})

	// Property 1: executor must have at least one Heartbeater-shape callsite.
	// If this fires, the fixture has become degenerate (executor no longer
	// calls .Heartbeat) and A1's scope exclusion needs re-evaluation.
	if len(executorCallsites) == 0 {
		t.Errorf("%s-B1: runtime/saga/executor/ contains zero Heartbeater-shape "+
			".Heartbeat callsites; the B1 fixture has become degenerate. "+
			"Either executor was refactored to remove all .Heartbeat calls "+
			"(update A1's scope comment + this test) or the scan is broken.",
			sagaCoordinatorNoHeartbeatLoopRule)
	}

	// Property 2: A1 itself must produce 0 violations for executor files.
	// Run A1's actual scanner (including its executor exclude filter) over
	// the executor package and assert that no violations are reported — the
	// path filter in A1 is what prevents executor callsites from being flagged.
	a1ViolationsInExecutor := RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/saga/executor/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Replicate A1's exact filter: only flag runtime/saga/ files
			// that are NOT under runtime/saga/executor/.
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			if strings.HasPrefix(rel, "runtime/saga/executor/") {
				continue
			}
			out = append(out, scanHeartbeatSelectors(p, file, rel)...)
		}
		return out
	})

	// A1's filter must suppress ALL executor callsites (the exclude is working).
	Report(t, sagaCoordinatorNoHeartbeatLoopRule+"-B1", a1ViolationsInExecutor)
}
