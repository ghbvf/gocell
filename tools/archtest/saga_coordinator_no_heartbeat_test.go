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
// the runtime/saga/executor/ subpackage — invokes journal.Journal.Heartbeat.
//
// Uses RunTyped because correctness requires resolving the method's
// receiver type to kernel/saga/journal.Journal — a string-anchor scan on
// the literal "Heartbeat" identifier would Soft-fail an unrelated future
// method named Heartbeat on a different type (e.g., http.Heartbeat helper).
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

			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				if sel.Sel == nil || sel.Sel.Name != heartbeatMethodName {
					return
				}
				fn, ok := ResolveMethodCall(p.TypesInfo, sel)
				if !ok || fn == nil {
					return
				}
				// Confirm the method belongs to kernel/saga/journal.Journal
				// (not some other interface that happens to also have a
				// Heartbeat method).
				if !methodFromJournalInterface(fn) {
					return
				}
				pos := p.Fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s: forbidden direct call to journal.Journal.Heartbeat in runtime/saga; "+
							"per-step lease maintenance is funneled through runtime/saga/executor "+
							"(Executor.Execute / Executor.RunWithHeartbeat). If you need a "+
							"non-step heartbeat (e.g. extending lease across an outer operation), "+
							"call Executor.RunWithHeartbeat — do NOT re-introduce a centralized "+
							"heartbeat goroutine.",
						sagaCoordinatorNoHeartbeatLoopRule),
				})
			})
		}
		return out
	})

	Report(t, sagaCoordinatorNoHeartbeatLoopRule+"-A1", diags)
}

// methodFromJournalInterface reports whether fn (a method func object) was
// declared on kernel/saga/journal.Journal. The receiver of a method on an
// interface is the interface type itself; we unwrap that to a *types.Named
// and check (Pkg.Path, Name) == (kernel/saga/journal, Journal).
//
// This pattern mirrors sagaJournalHolderSealRule's resolvedTypeIsJournal —
// kept inline (rather than calling out to that helper) so the two archtests
// can evolve independently if Journal moves packages, and so a misdirected
// future generalization can't accidentally relax both at once.
func methodFromJournalInterface(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	recv := sig.Recv()
	if recv == nil {
		return false
	}
	recvType := recv.Type()
	// Unwrap a pointer receiver (interfaces don't normally have pointer
	// receivers, but cover both shapes for defensive consistency with
	// resolvedTypeIsJournal).
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil {
		return false
	}
	if obj.Name() != journalInterfaceTypeName {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == journalInterfacePkgPath
}
