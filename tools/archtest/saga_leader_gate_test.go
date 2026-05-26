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
//	A3 [TestSagaLeaderGate_A3_LeadGatesDriveOne]
//	   The acquireLead boolean result (lead) MUST gate driveOne: there must be an
//	   IfStmt referencing lead that either early-exits the claim loop (the
//	   `if !lead { continue }` shape) or encloses the drive (`if lead { … }`). A
//	   blank/missing lead binding is an immediate violation. Closes the bypass
//	   A2 alone misses — call acquireLead, ignore lead, drive unconditionally.
//	   AI-robust: Medium (pure-AST control-dependency gate; same ceiling as
//	   A1/A2 — see below).
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
//   - B2 (semantic depth): formerly an open residual (A2 only proved acquireLead
//     was *called*). A3 now structurally requires the lead result to gate
//     driveOne, with red_tick_ignores_lead as the reverse self-test. The
//     deterministic unit test runtime/saga/leader_elect_test.go
//     (TestTickOnce_SkipsWhenLockHeld) remains as runtime corroboration.
//   - B3 (A3 indirection / partial gating, safe-side or unit-covered): A3
//     references lead directly, so an aliased guard (`ok := lead; if !ok {…}`)
//     would false-fire — safe-side, forces the canonical shape that production
//     uses. A3 is also satisfied by *one* lead-referencing guard, so a second,
//     unguarded driveOne in the same tickOnce would slip A3 (but A1 already
//     pins driveOne to exactly one site, and TestTickOnce_SkipsWhenLockHeld
//     exercises the real path). The compile-time-Hard closure of both remains
//     gh #1110 (typed gate token).
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

	violSagaLeaderA3LeadIgnored = "SAGA-DRIVE-BEHIND-LEADER-GATE-01-A3: " +
		"tickOnce calls acquireLead but its lead result does not gate driveOne — " +
		"the gate verdict must guard the drive (e.g. `if !lead { continue }`)"
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

// --- A3: acquireLead's lead result must gate driveOne ---

// TestSagaLeaderGate_A3_LeadGatesDriveOne asserts that in each tickOnce, the
// boolean result of acquireLead actually gates driveOne — closing the B2 gap
// that A2 (presence-only) leaves open: a tickOnce could call acquireLead,
// discard or ignore lead, and drive unconditionally (split-brain) while passing
// both A1 and A2.
func TestSagaLeaderGate_A3_LeadGatesDriveOne(t *testing.T) {
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
			ds = append(ds, checkLeaderGateA3(p, file)...)
		}
		return ds
	})
	Report(t, sagaLeaderGateRule+"-A3", diags)
}

// checkLeaderGateA3 emits a diagnostic for any tickOnce that calls driveOne and
// acquireLead but does not let the acquireLead boolean result (lead) gate the
// drive. "Gate" is approximated structurally: there must be an IfStmt in
// tickOnce whose condition references the lead variable and whose body either
// early-exits the claim loop (a BranchStmt / ReturnStmt — the `if !lead {
// continue }` shape) or directly encloses a driveOne call (the `if lead { …
// driveOne … }` shape). A blank/missing lead binding is an immediate violation
// (a discarded verdict cannot gate anything).
func checkLeaderGateA3(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != tickOnceFuncName || fd.Body == nil {
			return
		}
		// If this tickOnce never drives, A3 is vacuous (A1 governs drive sites).
		if !bodyCallsMethodNamed(fd.Body, driveOneMethodName) {
			return
		}
		leadVar, ok := acquireLeadResultVar(fd.Body)
		if !ok || !leadGatesDrive(fd.Body, leadVar) {
			pos := p.Fset.Position(fd.Pos())
			ds = append(ds, Diagnostic{
				Rel:     filepath.ToSlash(p.Rel(file)),
				Line:    pos.Line,
				Message: violSagaLeaderA3LeadIgnored,
			})
		}
	})
	return ds
}

// bodyCallsMethodNamed reports whether body contains a `<expr>.<name>(...)` call.
func bodyCallsMethodNamed(body *ast.BlockStmt, name string) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if callIsMethodNamed(call, name) {
			found = true
		}
	})
	return found
}

// acquireLeadResultVar finds the assignment whose RHS is `<expr>.acquireLead(...)`
// and returns the identifier bound to the second LHS (the lead bool). ok is
// false when acquireLead is not assigned to a 2-element tuple or the lead slot
// is blank (`_`) — a discarded verdict cannot gate the drive.
func acquireLeadResultVar(body *ast.BlockStmt) (name string, ok bool) {
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			return
		}
		call, isCall := as.Rhs[0].(*ast.CallExpr)
		if !isCall || !callIsMethodNamed(call, acquireLeadMethodName) {
			return
		}
		if id, isID := as.Lhs[1].(*ast.Ident); isID && id.Name != "_" {
			name, ok = id.Name, true
		}
	})
	return name, ok
}

// leadGatesDrive reports whether some IfStmt in body has a condition referencing
// leadVar and a body that either early-exits (BranchStmt/ReturnStmt) or contains
// a driveOne call — the two sanctioned gate shapes.
func leadGatesDrive(body *ast.BlockStmt, leadVar string) bool {
	gated := false
	EachInSubtree[ast.IfStmt](body, func(ifs *ast.IfStmt) {
		if gated || ifs.Cond == nil || !condReferences(ifs.Cond, leadVar) {
			return
		}
		if ifBodyEarlyExits(ifs.Body) || bodyCallsMethodNamed(ifs.Body, driveOneMethodName) {
			gated = true
		}
	})
	return gated
}

// condReferences reports whether expr contains an identifier named want.
func condReferences(expr ast.Expr, want string) bool {
	_, ok := FindFirstInSubtree[ast.Ident](expr, func(id *ast.Ident) bool {
		return id.Name == want
	})
	return ok
}

// ifBodyEarlyExits reports whether body contains a continue/break/return that
// skips the rest of the claim-loop iteration before driveOne runs.
func ifBodyEarlyExits(body *ast.BlockStmt) bool {
	exits := false
	EachInSubtree[ast.BranchStmt](body, func(*ast.BranchStmt) { exits = true })
	if exits {
		return true
	}
	EachInSubtree[ast.ReturnStmt](body, func(*ast.ReturnStmt) { exits = true })
	return exits
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

// TestSagaLeaderGate_Detector_RedTickIgnoresLead proves A3 fires when tickOnce
// calls acquireLead but ignores the lead result and drives unconditionally —
// the bypass A2 (presence-only) cannot catch.
func TestSagaLeaderGate_Detector_RedTickIgnoresLead(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaLeaderGateFixturePattern("red_tick_ignores_lead")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			ds = append(ds, checkLeaderGateA3(p, file)...)
		}
		return ds
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}
