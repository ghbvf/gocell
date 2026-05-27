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
// Downstream (callsite ban — this archtest): Medium. AST + typed analysis
// locates every method call to journal.Journal.Heartbeat in
// runtime/saga (the Coordinator's package — recursive scope intentionally
// includes only `./runtime/saga/.` not `.../...`, because the executor
// subpackage is the sanctioned caller). The check is "set must be empty"
// — any new call site fails CI.
//
// Upstream (type-system seal): MEDIUM (gh issue tracked). The
// kernel/saga/journal.Journal interface still declares Heartbeat as a
// method. As long as the Coordinator's `journal` field carries this
// interface type, the type system permits a Heartbeat call at the source
// level (it would be caught here, not at compile time). Hard upgrade
// path = split Journal into JournalCore + Heartbeater so the Coordinator
// field type lacks Heartbeat entirely — requires changing 22 conformance
// case + multi-impl signatures, out of scope for #1181.
//
// Follow-up issue: gh issue #1209 (split Journal interface into
// JournalCore + Heartbeater so the Coordinator field type lacks Heartbeat
// entirely, upgrading this archtest's upstream from Medium to Hard).
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
// Blind spot B1: a type alias `type X = journal.Journal` in runtime/saga
// would let the package reference `journal.Journal.Heartbeat` indirectly
// as `X.Heartbeat`. Mitigated by inheriting the alias ban from
// SAGA-JOURNAL-HOLDER-SEAL-01's B1 reverse self-test, which forbids
// `type X = journal.Journal` in runtime/saga.
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
	if !ok {
		return false
	}
	if sig.Recv() == nil {
		return false // not a method
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
