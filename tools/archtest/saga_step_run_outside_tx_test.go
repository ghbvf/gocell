// INVARIANT: SAGA-STEP-RUN-OUTSIDE-TX-01
//
// saga_step_run_outside_tx_test.go — call-discipline lock for the Coordinator
// step executor.
//
// Inside runtime/saga/coordinator.go, kernel/saga.StepFunc invocations are
// confined to the safeRun helper, AND safeRun MUST NOT be called from inside
// any TxRunner.RunInTx closure body. Together these ensure user step code
// never runs with a DB transaction held open.
//
// # Why ship in PR-03
//
// As soon as Coordinator + StepFunc exist, the call discipline is statically
// lockable. Earlier locking catches PR-06 retry-executor regressions
// immediately, before they reach main.
//
// # Two-layer lock
//
//	A1 [TestSagaStepRunOutsideTx_A1_StepFuncCallsiteUniqueness]
//	   Every CallExpr in runtime/saga/coordinator.go whose callee resolves
//	   to kernel/saga.StepFunc type MUST be inside the body of safeRun.
//	   AI-robust: Hard (typed callsite-uniqueness + posInRanges body gate;
//	   the (callee type, enclosing function) pair is unique — any other shape
//	   fails immediately).
//
//	A2 [TestSagaStepRunOutsideTx_A2_SafeRunNotInsideRunInTxClosure]
//	   For every CallExpr to RunInTx in coordinator.go, the closure literal
//	   passed as the second argument (the tx callback body) MUST NOT contain
//	   a call to safeRun. Checked via EachInSubtree over the closure body.
//	   AI-robust: Medium (AST structural check; covers direct safeRun(...) in
//	   the closure body; a helper-wrapper two levels deep is a B1 residual —
//	   documented below).
//
// # Blind spots (ai-robust 强制反向自检; reverse self-test below)
//
//	B1 [TestSagaStepRunOutsideTx_BlindSpot_B1_NoStepFuncAlias]
//	   A type alias `type S = ksaga.StepFunc` in runtime/saga/ would assign a
//	   different Go type to the callee, making A1's TypeOf check miss the call.
//	   Mitigated by: B1 reverse self-test scans all non-test .go files in
//	   runtime/saga/ for any TypeSpec whose underlying is a reference to saga.StepFunc.
//	   Rated Soft (string-anchor scan); tracked for Hard upgrade via typed
//	   StepFunc alias detection in gh issue #979.
//
//	B2 (undocumented runtime blind spot) — A2's EachInSubtree walks the closure
//	   literal syntactically passed to RunInTx. A pattern
//	     wrapper := func() { safeRun(...) }
//	     txRunner.RunInTx(ctx, func(txCtx context.Context) error { wrapper(); return nil })
//	   would have safeRun inside wrapper, not directly in the RunInTx closure —
//	   A2 would miss it. Mitigated by: file scope (coordinator.go only) +
//	   Coordinator single-authority pattern means no wrapper helpers exist today.
//	   A2 helper-function transitivity (safeRun callsite chase) tracked in
//	   gh issue #980 (shared call-graph technique with SAGA-STEP-COMPENSATE-PURE-01 B1).
//
// ref: tools/archtest/span_setattr_redact_test.go (callsite-uniqueness pattern)
// ref: tools/archtest/aftercommit_pure_transient_test.go (parent-node negative)
// ref: .claude/rules/gocell/ai-robust.md §"Hard 范本目录" "typed marker funnel"
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// --- Rule constants ---

const sagaStepRunOutsideTxRule = "SAGA-STEP-RUN-OUTSIDE-TX-01"

// safeRunFuncName is the single sanctioned StepFunc invoker inside coordinator.go.
const safeRunFuncName = "safeRun"

// runInTxMethodName is the method on TxRunner whose closure body must not
// contain a safeRun call.
const runInTxMethodName = "RunInTx"

// coordinatorGoRel is the single file enforced by A1 and A2. Both rules are
// intentionally narrow: only coordinator.go owns StepFunc dispatch.
const coordinatorGoRel = "runtime/saga/coordinator.go"

// sagaRuntimePkgPrefix is the prefix for runtime/saga files scanned by B1.
const sagaRuntimePkgPrefix = "runtime/saga/"

// ksagaPkgPath is the import path of kernel/saga, where StepFunc is declared.
const ksagaPkgPath = "github.com/ghbvf/gocell/kernel/saga"

// stepFuncTypeName is the declared type in ksagaPkgPath.
const stepFuncTypeName = "StepFunc"

// --- Violation messages ---

const (
	violSagaA1StepFuncOutsideSafeRun = "SAGA-STEP-RUN-OUTSIDE-TX-01-A1: " +
		"saga.StepFunc CallExpr outside safeRun body — " +
		"StepFunc must only be invoked from safeRun (user step code outside DB tx invariant)"

	violSagaA2SafeRunInsideRunInTx = "SAGA-STEP-RUN-OUTSIDE-TX-01-A2: " +
		"safeRun() called inside RunInTx closure body — " +
		"user step code would run with a DB transaction held open"

	violSagaB1StepFuncAlias = "SAGA-STEP-RUN-OUTSIDE-TX-01-B1 (blind-spot): " +
		"type alias of kernel/saga.StepFunc found in runtime/saga/ — " +
		"A1 type check would miss StepFunc invocations via this alias"
)

// --- A1: StepFunc callsite uniqueness (typed) ---

// TestSagaStepRunOutsideTx_A1_StepFuncCallsiteUniqueness asserts that every
// CallExpr in runtime/saga/coordinator.go whose callee resolves to type
// kernel/saga.StepFunc is inside the body of safeRun.
//
// Detection uses go/types TypesInfo to resolve the callee type: any CallExpr
// whose Fun has an underlying type matching the function signature of
// kernel/saga.StepFunc (resolved by walking p.Pkg's imports to the ksagaPkgPath
// package and looking up the StepFunc named type) must be in safeRun's body.
//
// Blind spots:
//   - A type alias in the same package would assign a different *types.Named,
//     so TypeOf would not match the ksagaPkgPath.StepFunc lookup. B1 reverse
//     self-test closes this gap syntactically.
//   - An indirect call through an interface method that has the same signature
//     is not caught — but coordinator.go does not define such interfaces.
func TestSagaStepRunOutsideTx_A1_StepFuncCallsiteUniqueness(t *testing.T) {
	t.Parallel()
	diags := RunTyped(t, TypedOpts{}, []string{"./runtime/saga/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		// Only enforce on coordinator.go.
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if rel != coordinatorGoRel {
				continue
			}
			ds = append(ds, checkA1StepFuncCallsites(p, file)...)
		}
		return ds
	})
	Report(t, sagaStepRunOutsideTxRule+"-A1", diags)
}

// checkA1StepFuncCallsites emits A1 diagnostics for coordinator.go:
// every CallExpr whose callee type is saga.StepFunc must be inside safeRun.
func checkA1StepFuncCallsites(p *Pass, file *ast.File) []Diagnostic {
	// Resolve the saga.StepFunc named type from the package's import closure.
	stepFuncType := resolveSagaStepFuncType(p.Pkg)
	if stepFuncType == nil {
		// Package doesn't import kernel/saga — nothing to check.
		return nil
	}

	// Collect the body Pos/End range of safeRun.
	safeRunRanges := collectFuncBodyRanges(file, safeRunFuncName)

	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !calleeIsSagaStepFunc(p.TypesInfo, call, stepFuncType) {
			return
		}
		if posInRanges(call.Pos(), safeRunRanges) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violSagaA1StepFuncOutsideSafeRun,
		})
	})
	return ds
}

// resolveSagaStepFuncType walks the import closure of pkg to find the
// kernel/saga package and returns the named type for StepFunc. Returns nil
// when the package is not imported.
func resolveSagaStepFuncType(pkg *types.Package) *types.Named {
	if pkg == nil {
		return nil
	}
	sagaPkg := findImportedPkg(pkg, ksagaPkgPath)
	if sagaPkg == nil {
		return nil
	}
	obj := sagaPkg.Scope().Lookup(stepFuncTypeName)
	if obj == nil {
		return nil
	}
	named, _ := obj.Type().(*types.Named)
	return named
}

// findImportedPkg does a BFS over pkg's transitive import closure to find the
// package with the given path. Returns nil if not found.
func findImportedPkg(root *types.Package, path string) *types.Package {
	seen := make(map[string]bool)
	var walk func(*types.Package) *types.Package
	walk = func(p *types.Package) *types.Package {
		if p.Path() == path {
			return p
		}
		if seen[p.Path()] {
			return nil
		}
		seen[p.Path()] = true
		for _, imp := range p.Imports() {
			if found := walk(imp); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(root)
}

// calleeIsSagaStepFunc reports whether the callee of call has the same
// underlying type as sagaStepFuncType. Handles both the exact named type and
// its underlying function signature.
func calleeIsSagaStepFunc(info *types.Info, call *ast.CallExpr, sagaStepFuncType *types.Named) bool {
	if info == nil || sagaStepFuncType == nil {
		return false
	}
	tv, ok := info.Types[call.Fun]
	if !ok {
		return false
	}
	t := tv.Type
	if t == nil {
		return false
	}
	// Direct named-type match (exact ksaga.StepFunc).
	if named, isNamed := t.(*types.Named); isNamed {
		return named == sagaStepFuncType
	}
	// Underlying signature match: a callee can be typed as the underlying func
	// signature without the named wrapper (e.g. after a type conversion).
	return types.Identical(t, sagaStepFuncType.Underlying())
}

// --- A2: safeRun not inside RunInTx closure (pure AST) ---

// TestSagaStepRunOutsideTx_A2_SafeRunNotInsideRunInTxClosure asserts that no
// RunInTx call in coordinator.go has a safeRun call directly inside its
// closure argument body.
//
// Detection: walk all CallExprs whose callee selector ends in "RunInTx". For
// each, inspect Args[1] (the closure literal). EachInSubtree inside the
// closure body for any CallExpr whose callee is the Ident "safeRun".
//
// Pure AST: no types needed — "RunInTx" selector match is syntactic.
//
// Residual blind spot B2: a wrapper helper defined outside the RunInTx
// closure that itself calls safeRun — A2's sub-tree scan does not chase
// helper bodies. Documented in package godoc; mitigated by coordinator.go
// single-authority scope.
func TestSagaStepRunOutsideTx_A2_SafeRunNotInsideRunInTxClosure(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if rel != coordinatorGoRel {
				continue
			}
			ds = append(ds, checkA2SafeRunNotInRunInTx(p, file)...)
		}
		return ds
	})
	Report(t, sagaStepRunOutsideTxRule+"-A2", diags)
}

// checkA2SafeRunNotInRunInTx walks the file for RunInTx calls and asserts
// their closure bodies do not contain a direct call to safeRun.
func checkA2SafeRunNotInRunInTx(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(outer *ast.CallExpr) {
		if !callIsRunInTx(outer) {
			return
		}
		// Args[1] must be the closure literal (the tx callback).
		if len(outer.Args) < 2 {
			return
		}
		lit, isLit := outer.Args[1].(*ast.FuncLit)
		if !isLit || lit.Body == nil {
			return
		}
		// Scan inside the closure body for any direct call to safeRun.
		EachInSubtree[ast.CallExpr](lit.Body, func(inner *ast.CallExpr) {
			if !callIsSafeRun(inner) {
				return
			}
			pos := p.Fset.Position(inner.Pos())
			ds = append(ds, Diagnostic{
				Rel:     filepath.ToSlash(p.Rel(file)),
				Line:    pos.Line,
				Message: violSagaA2SafeRunInsideRunInTx,
			})
		})
	})
	return ds
}

// callIsRunInTx reports whether call is `<expr>.RunInTx(...)` — the method
// name matched syntactically.
func callIsRunInTx(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == runInTxMethodName
}

// callIsSafeRun reports whether call is a direct (unqualified) call to the
// identifier "safeRun" — the only form that driveOne uses.
func callIsSafeRun(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == safeRunFuncName
}

// --- B1: No StepFunc alias in runtime/saga (pure AST, reverse self-test) ---

// TestSagaStepRunOutsideTx_BlindSpot_B1_NoStepFuncAlias asserts that no
// non-test .go file in runtime/saga/ declares a type alias of
// kernel/saga.StepFunc. A type alias `type S = ksaga.StepFunc` would give the
// callee a different *types.Named, bypassing A1's TypeOf match.
//
// Detection (pure AST): scan all TypeSpec nodes whose Type is an
// *ast.SelectorExpr matching `<pkg>.StepFunc` where `<pkg>` is the local
// import name of "github.com/ghbvf/gocell/kernel/saga". Both type alias
// (`type S = ...`) and type definition (`type S ksaga.StepFunc`) are reported
// — both create an escape path for A1.
//
// AST-only (no types): the file's import list is used to resolve the local
// name of the kernel/saga package. Tests files are excluded (they may
// legitimately re-type StepFunc for mocking).
//
// Rated Soft (string anchor on import path + selector name). The structural
// ceiling here is Medium — a type alias under a different local import name
// would be missed. Tracked for upgrade in a follow-up PR.
func TestSagaStepRunOutsideTx_BlindSpot_B1_NoStepFuncAlias(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			// Skip test files — mocks/fakes may redefine StepFunc-shaped types.
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Only scan runtime/saga/ files (not subdirectories of other layers
			// that happen to be in scope).
			if !strings.HasPrefix(rel, sagaRuntimePkgPrefix) {
				continue
			}
			ds = append(ds, checkB1NoStepFuncAlias(p, file)...)
		}
		return ds
	})
	Report(t, sagaStepRunOutsideTxRule+"-B1", diags)
}

// checkB1NoStepFuncAlias scans file for any TypeSpec that aliases or redefines
// ksaga.StepFunc using the file's local import name for kernel/saga.
func checkB1NoStepFuncAlias(p *Pass, file *ast.File) []Diagnostic {
	ksagaLocal := sagaLocalName(file)
	if ksagaLocal == "" {
		// File doesn't import kernel/saga at all — no alias possible.
		return nil
	}
	var ds []Diagnostic
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Type == nil {
			return
		}
		sel, ok := ts.Type.(*ast.SelectorExpr)
		if !ok {
			return
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != ksagaLocal {
			return
		}
		if sel.Sel == nil || sel.Sel.Name != stepFuncTypeName {
			return
		}
		pos := p.Fset.Position(ts.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violSagaB1StepFuncAlias,
		})
	})
	return ds
}

// sagaLocalName returns the local identifier used in file to refer to the
// kernel/saga package (default "saga" for an unnamed import; alias otherwise).
// Returns "" when the file does not import kernel/saga at all.
func sagaLocalName(file *ast.File) string {
	const sagaImportPath = `"github.com/ghbvf/gocell/kernel/saga"`
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != sagaImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		// Default local name is the last path segment.
		return "saga"
	}
	return ""
}

// posInRanges (package-level, defined in span_record_error_redact_test.go) and
// collectFuncBodyRanges (defined in span_setattr_redact_test.go) are reused
// directly — both are visible within the archtest package.
