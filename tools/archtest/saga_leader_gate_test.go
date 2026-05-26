// INVARIANT: SAGA-DRIVE-BEHIND-LEADER-GATE-01
//
// saga_leader_gate_test.go — leader-elect gate call-discipline lock for the
// Coordinator tick loop (PR-05, #964).
//
// The central correctness property of leader-elect is: no claimed instance is
// driven without first passing the distlock gate. This is enforced structurally
// in runtime/saga production files (excluding _test.go) by two layers:
//
//	A1 [TestSagaLeaderGate_A1_DriveOneOnlyInTickOnce]
//	   Every CallExpr `<recv>.driveOne(...)` MUST be lexically inside the body of
//	   the tickOnce function. A new driveOne caller anywhere else bypasses the
//	   gate → split-brain → A1 fires.
//	   AI-robust: Medium (pure-AST selector-name + enclosing-func-body gate;
//	   same shape as SAGA-STEP-RUN-OUTSIDE-TX-01 A2's callIsSafeRun/callIsRunInTx
//	   syntactic match).
//
//	A2 [TestSagaLeaderGate_A2_TickOnceCallsAcquireLead]
//	   The tickOnce function body MUST contain a call to acquireLead. The sole
//	   driveOne caller (per A1) must therefore consult the gate.
//	   AI-robust: Medium (pure-AST presence check).
//
// # Why ship in PR-05 (not deferred to PR-08 governance)
//
// Same logic the plan applies to SAGA-JOURNAL-HOLDER-SEAL-01 /
// SAGA-STEP-RUN-OUTSIDE-TX-01 (pulled to PR-03): as soon as the gate surface
// materializes (PR-05), the invariant is statically lockable. Shipping the
// feature without enforcement would leave the invariant a Soft single-callsite
// convention (ai-robust forbids new Soft).
//
// # AI-robust ceiling + blind spots (reverse self-tests below)
//
//   - Ceiling: Medium. A true Hard (compile-time inexpressible "drive without
//     gate") is unreachable in a single package — driveOne/acquireLead are
//     unexported methods on the same struct, and Go cannot prevent an in-package
//     caller from invoking an unexported method. Same ceiling rationale as
//     SAGA-JOURNAL-HOLDER-SEAL-01 (gh #981) and the SPAN holder-seal (gh #851).
//     Hard upgrade path (typed gate token threaded through driveOne's signature,
//     still package-internal) tracked in gh issue #1110.
//   - B1 (over-firing): A1 matches `.driveOne(` by selector name only. A method
//     named driveOne on a *different* type in runtime/saga would also be checked.
//     runtime/saga has exactly one driveOne (Coordinator); over-firing is
//     safe-side (would force a future second driveOne into tickOnce or a rename).
//   - B2 (semantic depth): A2 asserts acquireLead is *called* in tickOnce, not
//     that its boolean result actually short-circuits driveOne (the
//     `if !lead { continue }` shape). That semantic is covered by the
//     deterministic unit tests in runtime/saga/leader_elect_test.go
//     (TestTickOnce_SkipsWhenLockHeld). Documented residual.
//
// ref: tools/archtest/saga_step_run_outside_tx_test.go (callsite-discipline pattern)
// ref: .claude/rules/gocell/ai-robust.md §"Funnel 双向锁评级"
package archtest

import (
	"go/ast"
	"path/filepath"
	"testing"
)

const sagaLeaderGateRule = "SAGA-DRIVE-BEHIND-LEADER-GATE-01"

// driveOneMethodName is the Coordinator step-drive method gated by leader-elect.
const driveOneMethodName = "driveOne"

// acquireLeadMethodName is the leader-elect gate method tickOnce must call.
const acquireLeadMethodName = "acquireLead"

// tickOnceFuncName is the single sanctioned driveOne caller.
const tickOnceFuncName = "tickOnce"

// sagaLeaderGateFixturesDir is the testdata directory for red fixtures.
const sagaLeaderGateFixturesDir = "saga_leader_gate_fixtures"

const (
	violSagaLeaderA1DriveOutsideTick = "SAGA-DRIVE-BEHIND-LEADER-GATE-01-A1: " +
		"driveOne CallExpr outside tickOnce body — " +
		"driveOne must only be called from tickOnce so the leader-elect gate guards every drive"

	violSagaLeaderA2TickMissingGate = "SAGA-DRIVE-BEHIND-LEADER-GATE-01-A2: " +
		"tickOnce body does not call acquireLead — " +
		"the sole driveOne caller must pass the leader-elect gate"
)

// callIsMethodNamed reports whether call is `<expr>.<name>(...)` — selector
// method name matched syntactically (pure AST, no types).
func callIsMethodNamed(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == name
}

// --- A1: driveOne only inside tickOnce ---

// TestSagaLeaderGate_A1_DriveOneOnlyInTickOnce asserts every `.driveOne(`
// CallExpr in runtime/saga production files is inside the tickOnce body.
func TestSagaLeaderGate_A1_DriveOneOnlyInTickOnce(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isRuntimeSagaProductionFile(rel) {
				continue
			}
			ds = append(ds, checkLeaderGateA1(p, file)...)
		}
		return ds
	})
	Report(t, sagaLeaderGateRule+"-A1", diags)
}

// checkLeaderGateA1 emits a diagnostic for every driveOne callsite in file that
// is not within the tickOnce body range.
func checkLeaderGateA1(p *Pass, file *ast.File) []Diagnostic {
	tickRanges := collectFuncBodyRanges(file, tickOnceFuncName)
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !callIsMethodNamed(call, driveOneMethodName) {
			return
		}
		if posInRanges(call.Pos(), tickRanges) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violSagaLeaderA1DriveOutsideTick,
		})
	})
	return ds
}

// --- A2: tickOnce must call acquireLead ---

// TestSagaLeaderGate_A2_TickOnceCallsAcquireLead asserts each tickOnce function
// in runtime/saga production files calls acquireLead in its body.
func TestSagaLeaderGate_A2_TickOnceCallsAcquireLead(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isRuntimeSagaProductionFile(rel) {
				continue
			}
			ds = append(ds, checkLeaderGateA2(p, file)...)
		}
		return ds
	})
	Report(t, sagaLeaderGateRule+"-A2", diags)
}

// checkLeaderGateA2 emits a diagnostic for any tickOnce FuncDecl whose body
// does not contain a call to acquireLead.
func checkLeaderGateA2(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != tickOnceFuncName || fd.Body == nil {
			return
		}
		found := false
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if callIsMethodNamed(call, acquireLeadMethodName) {
				found = true
			}
		})
		if found {
			return
		}
		pos := p.Fset.Position(fd.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violSagaLeaderA2TickMissingGate,
		})
	})
	return ds
}

// --- reverse self-tests (red fixtures) ---

// sagaLeaderGateFixturePattern returns (relDir, pattern) for a fixture case.
func sagaLeaderGateFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaLeaderGateFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaLeaderGateFixturesDir + "/" + fix
}

// TestSagaLeaderGate_Detector_RedDriveOutsideTick proves A1 fires when driveOne
// is called from a function other than tickOnce.
func TestSagaLeaderGate_Detector_RedDriveOutsideTick(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaLeaderGateFixturePattern("red_drive_outside_tick")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			ds = append(ds, checkLeaderGateA1(p, file)...)
		}
		return ds
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaLeaderGate_Detector_RedTickMissingGate proves A2 fires when tickOnce
// drives without calling acquireLead.
func TestSagaLeaderGate_Detector_RedTickMissingGate(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaLeaderGateFixturePattern("red_tick_missing_gate")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			ds = append(ds, checkLeaderGateA2(p, file)...)
		}
		return ds
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}
