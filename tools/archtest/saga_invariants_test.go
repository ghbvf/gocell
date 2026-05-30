// invariants:
//   - INVARIANT: SAGA-STEP-COMPENSATE-PURE-01
//   - INVARIANT: SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01
//   - INVARIANT: SAGA-EXECUTOR-RAND-INJECTED-01
//   - INVARIANT: SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
//   - INVARIANT: SAGA-JOURNAL-HOLDER-SEAL-01
//   - INVARIANT: SAGA-DRIVE-BEHIND-LEADER-GATE-01
//   - INVARIANT: SAGA-STATUS-FANOUT-COVERAGE-01
//   - INVARIANT: SAGA-STEP-RUN-OUTSIDE-TX-01
//   - INVARIANT: SAGA-INVARIANTS-FILE-CONSOLIDATED-01
//   - INVARIANT: SAGA-CONSTRUCTOR-NIL-GUARD-01
//
// saga_invariants_test.go — consolidated saga-theme archtest invariants.
//
// Merged from (per .claude/rules/gocell/ai-robust.md §"archtest 文件命名"; Refs #1213):
//   - saga_compensate_pure_test.go                (SAGA-STEP-COMPENSATE-PURE-01)
//   - saga_coordinator_no_heartbeat_test.go       (SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01)
//   - saga_executor_rand_injected_test.go         (SAGA-EXECUTOR-RAND-INJECTED-01)
//   - saga_journal_conformance_enrollment_test.go (SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01)
//   - saga_journal_holder_seal_test.go            (SAGA-JOURNAL-HOLDER-SEAL-01)
//   - saga_leader_gate_test.go                    (SAGA-DRIVE-BEHIND-LEADER-GATE-01)
//   - saga_status_fanout_coverage_test.go         (SAGA-STATUS-FANOUT-COVERAGE-01)
//   - saga_step_run_outside_tx_test.go            (SAGA-STEP-RUN-OUTSIDE-TX-01)
//
// The 9th invariant, SAGA-INVARIANTS-FILE-CONSOLIDATED-01, is this file's own
// consolidation guard (defined inline at the end): the machine guard that keeps
// the saga theme from re-fragmenting. Its repo-wide generalization to every
// theme is tracked in #1279.
package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/codegen/sagacoveragegen"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// ============================================================================
// SAGA-STEP-COMPENSATE-PURE-01   (from saga_compensate_pure_test.go)
// ============================================================================
// INVARIANT: SAGA-STEP-COMPENSATE-PURE-01
//
// SAGA-STEP-COMPENSATE-PURE-01 — funnel guarding saga.CompensateFunc purity.
// Compensate is the pure-reverse rollback for one previously-committed step.
// The Coordinator owns the transaction layer; Compensate runs in the
// application domain only and MUST NOT perform durable/persistent side effects.
//
// # Detection mechanism
//
// Identify all production AST positions where a value is assigned to a
// kernel/saga.CompensateFunc-typed slot. Three assignment forms are covered:
//
//  1. var c saga.CompensateFunc = func(...) {...}    (ValueSpec — func literal)
//  2. c = func(...) {...} where c is CompensateFunc  (AssignStmt — func literal)
//  3. saga.Step{Compensate: func(...) {...}}          (CompositeLit KV — func literal)
//
// In addition, two named-function paths are covered:
//
//  4. var _ ksaga.CompensateFunc = myFunc            (ValueSpec — named func)
//  5. saga.Step{Compensate: myFunc}                  (CompositeLit KV — named func)
//
// Detection uses typed AST (RunTypedProduction / RunTypedFixture):
// the match is on the *declared type* of the LHS / struct field, resolved via
// TypesInfo.TypeOf / TypesInfo.Defs — NOT a string anchor on the name
// "Compensate".
//
// For function literals (forms 1-3): the body is scanned directly for
// forbidden calls.
//
// For named functions (forms 4-5): info.ObjectOf(ident).(*types.Func) resolves
// the declaration, and the FuncDecl body is scanned for forbidden calls.
//
// # Forbidden call set (closed)
//
//   - outbox.Writer.Write      (kernel/outbox.Writer interface)
//   - outbox.Emitter.Emit      (kernel/outbox.Emitter interface)
//   - persistence.TxRunner.RunInTx (kernel/persistence.TxRunner interface)
//   - *database/sql.Tx methods (Exec, Query, QueryRow, Commit, Rollback, etc.)
//   - pgx.Tx methods           (github.com/jackc/pgx/v5.Tx interface)
//
// Receiver type is resolved via TypesInfo.TypeOf(sel.X) + types.Implements for
// interfaces; concrete *sql.Tx matched by exact pkg+type-name after pointer deref.
//
// # AI-robust rating
//
// Hard primary path: CompensateFunc slot identified by declared type (not name);
// forbidden call set identified by types.Implements + exact pkg path (not string
// match). Import alias does not help the attacker.
//
// Medium upstream: helper-function transitivity via call-graph is NOT followed
// beyond the named-function one level. A CompensateFunc that calls a local
// helper (not itself typed as CompensateFunc) that then calls sql.Tx is NOT
// caught. Mitigation: B1 body-count discipline + the structural guarantee that
// Compensate receives no tx in its context. Call-graph upgrade tracked in
// gh issue #1182.
//
// # Blind spots (reverse self-test for each)
//
// B1 — helper-function transitivity: CompensateFunc body calls an unexported
//
//	local helper that itself touches a banned type. Not chased. Mitigated by
//	B1 body-count discipline: no production CompensateFunc literal exceeds
//	sagaMaxCompensateStmts top-level statements.
//
// B2 — generic/reflection: a CompensateFunc body using reflect.Value.MethodByName
//
//	to call a banned method. Not detected. Mitigated by reverse self-test
//	(TestSagaStepCompensatePure_BlindSpot_B2_NoMethodByNameInCompensateBodies)
//	which asserts no production CompensateFunc body uses MethodByName.
//	Call-graph upgrade tracked in gh issue #1182.
//
// B3 — CompensateFunc passed as a function parameter (not an assignment):
//
//	e.g. runComp(myComp) where param type is CompensateFunc. The function body
//	of myComp is NOT currently scanned. This is a known gap; the named-func
//	path (forms 4-5) covers the most common idiom (assignment to typed slot).
//	Reverse self-test (TestSagaStepCompensatePure_BlindSpot_B3_NoCompensateFuncParam)
//	asserts no production code passes a CompensateFunc value as a function argument
//	(call-graph upgrade tracked in gh issue #1182).
//
// ref: tools/archtest/aftercommit_pure_transient_test.go (sibling purity pattern).
// ref: .claude/rules/gocell/ai-robust.md §"typed marker funnel for unbounded ops".

const (
	sagaCompensatePureRuleID      = "SAGA-STEP-COMPENSATE-PURE-01"
	sagaPkgPath                   = "github.com/ghbvf/gocell/kernel/saga"
	sagaCompensateFuncName        = "CompensateFunc"
	sagaCompensateFieldName       = "Compensate"
	sagaFixturesDir               = "saga_compensate_pure_fixtures"
	sagaMaxCompensateStmts        = 10
	sagaConstructorNilGuardRuleID = "SAGA-CONSTRUCTOR-NIL-GUARD-01"
	sagaConstructorFixturesDir    = "saga_constructor_nilguard_fixtures"
)

// sagaBannedReceiverKeys maps "<pkgpath>.<TypeName>" → display label for
// concrete banned types (matched after one pointer deref for *sql.Tx).
var sagaBannedReceiverKeys = map[string]string{
	"database/sql.Tx":                               "*sql.Tx",
	"github.com/jackc/pgx/v5.Tx":                    "pgx.Tx",
	"github.com/ghbvf/gocell/kernel/outbox.Writer":  "outbox.Writer",
	"github.com/ghbvf/gocell/kernel/outbox.Emitter": "outbox.Emitter",
}

// sagaBannedIfaceSpecs lists the (pkgPath, typeName, label) tuples for
// interface types that CompensateFunc bodies must not call methods on.
var sagaBannedIfaceSpecs = []struct{ path, name, label string }{
	{"github.com/jackc/pgx/v5", "Tx", "pgx.Tx"},
	{"github.com/ghbvf/gocell/kernel/outbox", "Writer", "outbox.Writer"},
	{"github.com/ghbvf/gocell/kernel/outbox", "Emitter", "outbox.Emitter"},
	{"github.com/ghbvf/gocell/kernel/persistence", "TxRunner", "persistence.TxRunner"},
}

// sagaResolveBannedReceivers resolves the banned interface receiver types from
// pkg's transitive import closure, once per Pass.
func sagaResolveBannedReceivers(pkg *types.Package) []bannedReceiver {
	var out []bannedReceiver
	for _, b := range sagaBannedIfaceSpecs {
		if iface := lookupInterface(pkg, b.path, b.name); iface != nil {
			out = append(out, bannedReceiver{label: b.label, iface: iface})
		}
	}
	return out
}

// sagaBannedReceiverLabel reports the display label when sel.X's static type
// is a banned receiver. It checks:
//  1. Exact concrete match (deref one pointer level for *sql.Tx).
//  2. Implements a banned interface (types.Implements).
func sagaBannedReceiverLabel(info *types.Info, sel *ast.SelectorExpr, ifaces []bannedReceiver) (string, bool) {
	t := info.TypeOf(sel.X)
	if t == nil {
		return "", false
	}
	// 1. Exact concrete match (deref one pointer level for *sql.Tx / *pgx.Tx).
	nt := t
	if ptr, isPtr := nt.(*types.Pointer); isPtr {
		nt = ptr.Elem()
	}
	if named, isNamed := nt.(*types.Named); isNamed && named.Obj() != nil && named.Obj().Pkg() != nil {
		key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		if label, ok := sagaBannedReceiverKeys[key]; ok {
			return label, true
		}
	}
	// 2. Implements a banned interface (value or pointer receiver).
	for _, b := range ifaces {
		if implementsBannedIface(t, b.iface) {
			return b.label, true
		}
	}
	return "", false
}

// isCompensateFuncType reports whether typ is exactly kernel/saga.CompensateFunc.
func isCompensateFuncType(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == sagaPkgPath &&
		obj.Name() == sagaCompensateFuncName
}

// compensateFuncAssignment holds a scanned assignment: either a func literal
// body (for direct scan) or an ident reference to a named function (for
// declared-func scan). Exactly one of Lit / NamedIdent is non-nil.
type compensateFuncAssignment struct {
	Lit        *ast.FuncLit
	NamedIdent *ast.Ident
}

// collectCompensateAssignments walks the AST of file and returns every
// assignment to a kernel/saga.CompensateFunc-typed slot, covering:
//
//  1. var c saga.CompensateFunc = <expr>    (ValueSpec)
//  2. c = <expr> where c is CompensateFunc (AssignStmt)
//  3. saga.Step{Compensate: <expr>}         (CompositeLit KV)
//
// <expr> is either a FuncLit (returned in Lit) or an Ident referencing a
// named function (returned in NamedIdent).
func collectCompensateAssignments(p *Pass, file *ast.File) []compensateFuncAssignment {
	info := p.TypesInfo
	var out []compensateFuncAssignment

	// var c saga.CompensateFunc = <expr>
	scanner.EachInSubtree[ast.ValueSpec](file, func(node *ast.ValueSpec) {
		typ := info.TypeOf(node.Type)
		if typ == nil || !isCompensateFuncType(typ) {
			return
		}
		for _, val := range node.Values {
			out = append(out, classifyExpr(val))
		}
	})

	// c = <expr> where c is CompensateFunc
	scanner.EachInSubtree[ast.AssignStmt](file, func(node *ast.AssignStmt) {
		for i, lhs := range node.Lhs {
			if i >= len(node.Rhs) {
				break
			}
			lhsType := info.TypeOf(lhs)
			if lhsType == nil || !isCompensateFuncType(lhsType) {
				continue
			}
			out = append(out, classifyExpr(node.Rhs[i]))
		}
	})

	// saga.Step{Compensate: <expr>}
	scanner.EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
		scanner.EachInChildren[ast.KeyValueExpr](node, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != sagaCompensateFieldName {
				return
			}
			// Verify the field's declared type is CompensateFunc via struct
			// field lookup (TypeOf on a CompositeLit KV value may return the
			// underlying func signature, not the named alias).
			if !compensateKVFieldIsCompensateFunc(info, node) {
				return
			}
			out = append(out, classifyExpr(kv.Value))
		})
	})

	return out
}

// compensateKVFieldIsCompensateFunc verifies that the struct type of the
// CompositeLit node has a field named sagaCompensateFieldName whose declared
// type is kernel/saga.CompensateFunc. This is the reliable path for CompositeLit
// KV values where TypeOf(kv.Value) may return the underlying func signature.
func compensateKVFieldIsCompensateFunc(info *types.Info, node *ast.CompositeLit) bool {
	structType := info.TypeOf(node)
	if structType == nil {
		return false
	}
	st := structType
	if ptr, ok := st.(*types.Pointer); ok {
		st = ptr.Elem()
	}
	var underlying types.Type
	if named, ok := st.(*types.Named); ok {
		underlying = named.Underlying()
	} else {
		underlying = st.Underlying()
	}
	s, ok := underlying.(*types.Struct)
	if !ok {
		return false
	}
	for fi := 0; fi < s.NumFields(); fi++ {
		f := s.Field(fi)
		if f.Name() == sagaCompensateFieldName && isCompensateFuncType(f.Type()) {
			return true
		}
	}
	return false
}

// classifyExpr returns a compensateFuncAssignment for expr: either a FuncLit
// or a named-function Ident.
func classifyExpr(expr ast.Expr) compensateFuncAssignment {
	switch e := expr.(type) {
	case *ast.FuncLit:
		return compensateFuncAssignment{Lit: e}
	case *ast.Ident:
		return compensateFuncAssignment{NamedIdent: e}
	}
	return compensateFuncAssignment{}
}

// sagaFuncDeclsByName collects all top-level FuncDecls in the Pass (across
// all files) keyed by the types.Func pointer (via info.ObjectOf). This is
// used to resolve named CompensateFunc assignments to their declaration bodies.
func sagaFuncDeclsByObject(p *Pass) map[*types.Func]*ast.FuncDecl {
	out := make(map[*types.Func]*ast.FuncDecl)
	for _, f := range p.Files {
		scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			obj, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
			if !ok {
				return
			}
			out[obj] = fd
		})
	}
	return out
}

// countStmts counts the top-level statements in a BlockStmt.
func countStmts(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}
	return len(body.List)
}

func sagaDiag(p *Pass, node ast.Node, rel, msg string) Diagnostic {
	return Diagnostic{Rel: rel, Line: p.Fset.Position(node.Pos()).Line, Message: msg}
}

// scanBodyForForbiddenCalls scans a BlockStmt for calls to banned receivers
// and returns diagnostics.
func scanBodyForForbiddenCalls(p *Pass, body *ast.BlockStmt, rel string, ifaces []bannedReceiver) []Diagnostic {
	info := p.TypesInfo
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return
		}
		if label, banned := sagaBannedReceiverLabel(info, sel, ifaces); banned {
			out = append(out, sagaDiag(p, sel, rel,
				sagaCompensatePureRuleID+"-A1: CompensateFunc body must be pure-reverse; "+
					"it calls a method on "+label+
					" (Compensate runs in app domain only — the Coordinator owns the tx layer)"))
		}
	})
	return out
}

// scanCompensatePure implements A1 over one file: collect every CompensateFunc
// assignment (literal or named func) and check the body for forbidden calls.
func scanCompensatePure(p *Pass, file *ast.File, ifaces []bannedReceiver, funcDecls map[*types.Func]*ast.FuncDecl) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic

	assignments := collectCompensateAssignments(p, file)
	for _, a := range assignments {
		switch {
		case a.Lit != nil:
			// Direct function literal: scan its body.
			out = append(out, scanBodyForForbiddenCalls(p, a.Lit.Body, rel, ifaces)...)

		case a.NamedIdent != nil:
			// Named function: resolve to FuncDecl and scan its body.
			obj, ok := p.TypesInfo.ObjectOf(a.NamedIdent).(*types.Func)
			if !ok {
				continue
			}
			fd, ok := funcDecls[obj]
			if !ok {
				continue
			}
			out = append(out, scanBodyForForbiddenCalls(p, fd.Body, rel, ifaces)...)
		}
	}
	return out
}

// scanCompensateBodyCount implements B1 over one file: every CompensateFunc
// literal must have <= sagaMaxCompensateStmts top-level statements.
func scanCompensateBodyCount(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic

	assignments := collectCompensateAssignments(p, file)
	for _, a := range assignments {
		if a.Lit == nil {
			continue // only applies to inline literals
		}
		n := countStmts(a.Lit.Body)
		if n > sagaMaxCompensateStmts {
			out = append(out, sagaDiag(p, a.Lit, rel,
				sagaCompensatePureRuleID+"-B1: CompensateFunc body has too many statements ("+
					itoa(n)+">"+itoa(sagaMaxCompensateStmts)+"); "+
					"pure-reverse compensates must be short — extract logic to a named helper "+
					"and call it from Compensate (B1 blind-spot discipline)"))
		}
	}
	return out
}

func sagaCompensateFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaFixturesDir + "/" + fix
}

// TestSagaStepCompensatePure_A1_NoForbiddenCallsInCompensateBody asserts no
// production CompensateFunc slot (literal or named func) calls a banned receiver
// method. In PR-06 there are zero CompensateFunc assignments in production, so
// this fires 0 diagnostics; it guards future Compensate authors.
func TestSagaStepCompensatePure_A1_NoForbiddenCallsInCompensateBody(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			// Skip test files — tests may use CompensateFunc with banned types
			// for mocking/conformance purposes.
			if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
				continue
			}
			out = append(out, scanCompensatePure(p, file, ifaces, funcDecls)...)
		}
		return out
	})
	Report(t, sagaCompensatePureRuleID+"-A1", diags)
}

// TestSagaStepCompensatePure_BlindSpot_B1_BodyStatementCount asserts no
// production CompensateFunc literal exceeds sagaMaxCompensateStmts top-level
// statements. Pure compensates must be short; a long body suggests a helper
// call that may hide forbidden calls (B1 mitigation).
func TestSagaStepCompensatePure_BlindSpot_B1_BodyStatementCount(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
				continue
			}
			out = append(out, scanCompensateBodyCount(p, file)...)
		}
		return out
	})
	Report(t, sagaCompensatePureRuleID+"-B1", diags)
}

// TestSagaStepCompensatePure_BlindSpot_B2_NoMethodByNameInCompensateBodies is
// the reverse self-test for blind spot B2 (reflection via MethodByName).
// It asserts that no production CompensateFunc body (literal or named) contains
// a call to reflect.Value.MethodByName or reflect.Type.MethodByName, which
// would allow bypassing the banned-receiver check. Production currently has
// zero CompensateFunc assignments so this always passes; the test guards future
// authors. Call-graph upgrade tracked in gh issue #1182.
func TestSagaStepCompensatePure_BlindSpot_B2_NoMethodByNameInCompensateBodies(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
				continue
			}
			assignments := collectCompensateAssignments(p, file)
			rel := p.Rel(file)
			for _, a := range assignments {
				var body *ast.BlockStmt
				switch {
				case a.Lit != nil:
					body = a.Lit.Body
				case a.NamedIdent != nil:
					if obj, ok := p.TypesInfo.ObjectOf(a.NamedIdent).(*types.Func); ok {
						if fd, ok2 := funcDecls[obj]; ok2 {
							body = fd.Body
						}
					}
				}
				if body == nil {
					continue
				}
				EachInSubtree[ast.SelectorExpr](body, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name == "MethodByName" {
						out = append(out, sagaDiag(p, sel, rel,
							sagaCompensatePureRuleID+"-B2 (blind-spot guard): "+
								"MethodByName call inside a CompensateFunc body; "+
								"reflection-based dispatch is a B2 blind spot — "+
								"call-graph upgrade tracked in gh issue #1182"))
					}
				})
			}
		}
		return out
	})
	Report(t, sagaCompensatePureRuleID+"-B2", diags)
}

// TestSagaStepCompensatePure_BlindSpot_B3_NoCompensateFuncPassedAsArgument is
// the reverse self-test for blind spot B3 (CompensateFunc passed as a function
// argument rather than assigned to a typed slot). The scan asserts that no
// production CallExpr outside the executor's own safeRunCompensate transport
// layer passes a value of type kernel/saga.CompensateFunc as an argument —
// such a pattern is the main B3 escape path and is disallowed until the
// call-graph upgrade in gh issue #1182 is complete.
//
// Exclusion: runtime/saga/executor/executor.go is the single sanctioned site
// that calls safeRunCompensate(ctx, step.Compensate, ...) — this is the
// framework transport funnel, not business code. All other call sites are
// forbidden.
func TestSagaStepCompensatePure_BlindSpot_B3_NoCompensateFuncPassedAsArgument(t *testing.T) {
	t.Parallel()
	// The one sanctioned call site: executor passes step.Compensate to safeRunCompensate.
	const sanctionedCallee = "safeRunCompensate"
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				// Exclude the sanctioned safeRunCompensate call in the executor transport layer.
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == sanctionedCallee {
					return
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == sanctionedCallee {
					return
				}
				for _, arg := range call.Args {
					if typ := p.TypesInfo.TypeOf(arg); typ != nil && isCompensateFuncType(typ) {
						out = append(out, sagaDiag(p, arg, p.Rel(file),
							sagaCompensatePureRuleID+"-B3 (blind-spot guard): "+
								"CompensateFunc value passed as a function argument outside "+
								"the safeRunCompensate transport funnel; "+
								"B3 bodies are not scanned — use the typed-slot assignment forms "+
								"(forms 1–5) until call-graph support lands (gh issue #1182)"))
					}
				}
			})
		}
		return out
	})
	Report(t, sagaCompensatePureRuleID+"-B3", diags)
}

// TestSagaStepCompensatePure_Detector_RedOutboxCallFixture loads the
// red_outbox_call fixture and asserts the A1 detector fires with the expected
// diagnostic. This is the detector self-test for the func-literal path: if A1's
// detection logic is broken, this test fails even though the production scan
// yields 0 diagnostics.
func TestSagaStepCompensatePure_Detector_RedOutboxCallFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaCompensateFixturePattern("red_outbox_call")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensatePure(p, file, ifaces, funcDecls)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaStepCompensatePure_Detector_RedNamedFuncFixture loads the
// red_named_func fixture and asserts the A1 detector fires with the expected
// diagnostic. This is the detector self-test for the named-function path.
func TestSagaStepCompensatePure_Detector_RedNamedFuncFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaCompensateFixturePattern("red_named_func")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensatePure(p, file, ifaces, funcDecls)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// runCompensatePureFixture loads a compensate-pure fixture dir and runs the A1
// scan over it, returning the diagnostics for golden assertion.
func runCompensatePureFixture(t *testing.T, fixture string) (string, []Diagnostic) {
	t.Helper()
	root := findModuleRoot(t)
	relDir, pattern := sagaCompensateFixturePattern(fixture)
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensatePure(p, file, ifaces, funcDecls)...)
		}
		return out
	})
	return filepath.Join(root, relDir, "diag.golden"), diags
}

// TestSagaStepCompensatePure_Detector_RedValueSpecFuncLitFixture loads the
// red_valuespec_funclit fixture (form 1: `var c saga.CompensateFunc = func(){}`)
// and asserts the A1 detector fires. Closes the form-1 fixture gap: prior to
// this fixture the ValueSpec-with-func-literal path was asserted by no golden.
func TestSagaStepCompensatePure_Detector_RedValueSpecFuncLitFixture(t *testing.T) {
	goldenPath, diags := runCompensatePureFixture(t, "red_valuespec_funclit")
	AssertGolden(t, goldenPath, diags)
}

// TestSagaStepCompensatePure_Detector_RedAssignFuncLitFixture loads the
// red_assign_funclit fixture (form 2: `c = func(){}` AssignStmt) and asserts the
// A1 detector fires. Closes the form-2 fixture gap.
func TestSagaStepCompensatePure_Detector_RedAssignFuncLitFixture(t *testing.T) {
	goldenPath, diags := runCompensatePureFixture(t, "red_assign_funclit")
	AssertGolden(t, goldenPath, diags)
}

// ============================================================================
// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01   (from saga_coordinator_no_heartbeat_test.go)
// ============================================================================
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

// ============================================================================
// SAGA-EXECUTOR-RAND-INJECTED-01   (from saga_executor_rand_injected_test.go)
// ============================================================================
// INVARIANT: SAGA-EXECUTOR-RAND-INJECTED-01
//
// SAGA-EXECUTOR-RAND-INJECTED-01 — funnel guarding random-source injection
// inside runtime/saga/executor.
//
// # Rule
//
// In every production .go file under runtime/saga/executor/ (excluding _test.go),
// package-level global functions from math/rand and math/rand/v2 are forbidden.
// The only allowed calls to these packages are explicit source constructors:
// rand.New, rand.NewPCG, rand.NewChaCha8 (math/rand/v2) and rand.New,
// rand.NewSource (math/rand). All other package-level functions
// (rand.Int64N, rand.Intn, rand.Float64, rand.Int63n, rand.Seed,
// rand.Shuffle, rand.Read, etc.) are forbidden.
//
// # Rationale
//
// Package-level global random functions use a shared global source that cannot
// be seeded or replaced per-test. The executor must inject its random source
// (passed at construction via WithJitterSource or similar) so that retry jitter
// is deterministic in tests and reproducible in production. Using the global
// source violates the clock-injection discipline: correctness in tests depends
// on controlling the source of non-determinism.
//
// # Detection mechanism
//
// Typed AST (RunTypedProduction): scan every CallExpr whose callee is a
// *ast.SelectorExpr. Resolve the callee Ident via TypesInfo.ObjectOf to a
// *types.Func. If the function's package path is "math/rand" or "math/rand/v2"
// AND it has a nil receiver (package-level function) AND its name is NOT in the
// allowed constructor set, report a violation.
//
// This is the same pattern used by PROD-CLOCK-INJECTION-01 for time.Now:
// type-driven via info.ObjectOf, immune to import aliases and dot-imports.
//
// # Allowed constructor set (package-level functions NOT flagged)
//
//   - math/rand/v2: New, NewPCG, NewChaCha8
//   - math/rand (v1): New, NewSource
//
// # AI-robust rating
//
// Hard downstream: CallExpr resolution uses TypesInfo.ObjectOf (types.Func +
// pkg path check + nil-receiver check). The allowed set is keyed by exact
// (pkg path, func name) pair — import alias does not help, since ObjectOf
// resolves to the canonical package regardless of local alias.
//
// Medium upstream: archtest locks call sites within the scanned package. It is
// not a type-system constraint (no sealed interface). A helper package outside
// runtime/saga/executor/ that calls the global rand and whose result is used by
// the executor is NOT caught by this rule. That indirect path is documented as
// a known blind spot (B1) tracked in gh issue #1183.
//
// # Blind spots (reverse self-tests below)
//
// B1 — indirect: executor imports a helper package that itself calls
//
//	rand.Int64N. Not detected by this rule (scope is executor package only).
//	Mitigated: helper packages in runtime/saga/ are also scoped by this rule
//	if they reside in runtime/saga/executor/. Cross-package helpers outside
//	that scope require call-graph analysis — deferred.
//
// B2 — dot-import: `import . "math/rand/v2"; Int64N(10)` — the Ident is the
//
//	call function reference directly (no SelectorExpr). HANDLED: the scanner
//	also walks ast.Ident nodes via info.ObjectOf to catch dot-imports, same
//	as PROD-CLOCK-INJECTION-01. Reverse self-test: production tree asserts
//	no dot-import of math/rand or math/rand/v2 exists in runtime/saga/executor/.
//
// ref: tools/archtest/clock_invariants_test.go (PROD-CLOCK-INJECTION-01, sibling
//
//	pattern for time.Now injection discipline).
//
// ref: .claude/rules/gocell/ai-robust.md §"typed marker funnel for unbounded ops".

const (
	sagaRandInjectedRuleID = "SAGA-EXECUTOR-RAND-INJECTED-01"
	// executorPkgPrefix is the module-relative path prefix for the
	// runtime/saga/executor package (production .go files, not _test.go).
	executorPkgPrefix = "runtime/saga/executor/"
)

// randV1PkgPath and randV2PkgPath are the canonical import paths for the two
// math/rand package versions. Both are checked so v1 usage is also caught.
const (
	randV1PkgPath = "math/rand"
	randV2PkgPath = "math/rand/v2"
)

// randAllowedConstructors is the set of function names that ARE permitted as
// package-level calls (they construct a source, not use the global source).
// Keyed by (pkgPath, funcName).
var randAllowedConstructors = map[string]bool{
	randV2PkgPath + ".New":        true,
	randV2PkgPath + ".NewPCG":     true,
	randV2PkgPath + ".NewChaCha8": true,
	randV1PkgPath + ".New":        true,
	randV1PkgPath + ".NewSource":  true,
}

// isRandGlobalForbidden reports whether fn is a package-level function in
// math/rand or math/rand/v2 that is NOT in the allowed constructor set.
func isRandGlobalForbidden(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	pkgPath := fn.Pkg().Path()
	if pkgPath != randV1PkgPath && pkgPath != randV2PkgPath {
		return false
	}
	// Must be a package-level function (nil receiver), not a method.
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	key := pkgPath + "." + fn.Name()
	return !randAllowedConstructors[key]
}

// scanRandGlobalCalls walks file's AST and returns diagnostics for every
// call to a forbidden package-level global rand function. Detection is
// type-driven via info.ObjectOf — covers both qualified form (rand.Int64N)
// and dot-import form (Int64N after `import . "math/rand/v2"`).
func scanRandGlobalCalls(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	info := p.TypesInfo
	var out []Diagnostic
	seen := map[string]bool{}

	record := func(node ast.Node, fnName, pkgPath string) {
		line := p.Fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s.%s", rel, line, pkgPath, fnName)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				sagaRandInjectedRuleID+": %s.%s — "+
					"must use an injected *rand.Rand source (rand.New(rand.NewPCG(...))) "+
					"instead of the package-level global; "+
					"global rand is not injectable and makes jitter non-deterministic in tests",
				pkgPath, fnName,
			),
		})
	}

	// Qualified form: rand.Int64N(...)
	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		fn, ok := info.ObjectOf(e.Sel).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	// Dot-import form: Int64N(...) — bare Ident, no SelectorExpr.
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		fn, ok := info.ObjectOf(e).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// isExecutorProductionFile reports whether rel is a production .go file
// under runtime/saga/executor/ (not _test.go).
func isExecutorProductionFile(rel string) bool {
	return strings.HasPrefix(rel, executorPkgPrefix) && !strings.HasSuffix(rel, "_test.go")
}

// sagaExecutorRandFixturePattern returns the (relDir, pattern) pair for the
// given fixture case under saga_executor_rand_injected_fixtures.
func sagaExecutorRandFixturePattern(fix string) (dir, pattern string) {
	const fixturesDir = "saga_executor_rand_injected_fixtures"
	return filepath.Join("tools", "archtest", "testdata", fixturesDir, fix),
		"./tools/archtest/testdata/" + fixturesDir + "/" + fix
}

// TestSagaExecutorRandInjected_A1_NoGlobalRandInExecutor asserts that no
// production file under runtime/saga/executor/ calls a package-level global
// function from math/rand or math/rand/v2 (other than the allowed constructors
// rand.New / rand.NewPCG / rand.NewChaCha8 / rand.NewSource). This ensures that
// jitter randomness is injected and deterministic in tests.
//
// Blind spots documented in file-level godoc:
//   - B1: cross-package helpers — not scanned (scope = executor/ only).
//   - B2: dot-import — handled via ast.Ident walk (reverse self-test below).
func TestSagaExecutorRandInjected_A1_NoGlobalRandInExecutor(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isExecutorProductionFile(rel) {
				continue
			}
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	Report(t, sagaRandInjectedRuleID+"-A1", diags)
}

// TestSagaExecutorRandInjected_BlindSpot_B2_NoDotImportRandInExecutor is the
// reverse self-test for blind spot B2 (dot-import of math/rand). It asserts
// that no production executor file uses a dot-import of math/rand or
// math/rand/v2, which would create an unlabeled call-site not covered by the
// SelectorExpr branch alone (though scanRandGlobalCalls's Ident walk handles it).
// The Ident-walk coverage means B2 is de-facto caught; this test asserts the
// absence of the pattern so future authors know it's disallowed.
//
// AST-only (pure): scanning import specs for dot-import is sufficient without
// type loading.
func TestSagaExecutorRandInjected_BlindSpot_B2_NoDotImportRandInExecutor(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga/executor"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, executorPkgPrefix) {
				continue
			}
			for _, imp := range file.Imports {
				if imp.Name == nil || imp.Name.Name != "." {
					continue
				}
				path := ""
				if imp.Path != nil {
					path = imp.Path.Value
				}
				if path == `"`+randV1PkgPath+`"` || path == `"`+randV2PkgPath+`"` {
					pos := p.Fset.Position(imp.Pos())
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: sagaRandInjectedRuleID + "-B2 (blind-spot guard): " +
							"dot-import of math/rand or math/rand/v2 found in executor; " +
							"use qualified form `rand.Int64N(...)` (which is still forbidden) " +
							"or the injected source instead",
					})
				}
			}
		}
		return out
	})
	Report(t, sagaRandInjectedRuleID+"-B2", diags)
}

// TestSagaExecutorRandInjected_Detector_RedGlobalRandFixture loads the
// red_global_rand fixture and asserts the A1 detector fires with the expected
// diagnostic. This is the detector self-test: if A1's detection logic is
// broken, this test fails even though the production scan yields 0 diagnostics.
func TestSagaExecutorRandInjected_Detector_RedGlobalRandFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaExecutorRandFixturePattern("red_global_rand")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		// No scope filter for fixtures: scan all loaded files.
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaExecutorRandInjected_Detector_RedDotImportRandFixture loads the
// red_dot_import_rand fixture (dot-imported `Int64N(...)` as a bare Ident) and
// asserts the A1 detector fires via its ast.Ident walk branch. Closes the B2
// fixture gap: prior to this fixture the dot-import branch had only the reverse
// self-test (asserting absence in production), not a positive detector proof.
func TestSagaExecutorRandInjected_Detector_RedDotImportRandFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaExecutorRandFixturePattern("red_dot_import_rand")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// ============================================================================
// SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01   (from saga_journal_conformance_enrollment_test.go)
// ============================================================================
// INVARIANT: SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware；identifies every
//     concrete named type (exported AND unexported) that satisfies
//     kernel/saga/journal.Journal (value or pointer receivers).
//   - conformance 调用扫描 (impl-level): ResolvePackageRef + _test.go path filter
//     plus per-call return-type unwrap — type-aware callee resolution via
//     *types.Info. For every _test.go file that calls
//     sagajournaltest.RunConformanceSuite, walk every CallExpr in the file and
//     unwrap its return tuple; impls whose key matches the impl set are marked
//     enrolled. Package co-location alone no longer credits enrollment — the
//     test file must actually construct the impl.
//   - 综合 Medium 天花板: Go cannot require a _test.go file to exist for a type at
//     compile time. The enforcement is archtest-bound (CI fails), not
//     compile-time. The Hard upgrade path is a codegen funnel + golden that
//     enumerates Journal impls from a single source and diff-locks the registry;
//     deferred as cross-PR governance work — tracked in gh issue #1003. The
//     mirror precedent is USERREPO-CONFORMANCE-ENROLLMENT-01.
//
// Enforces: every concrete type in the production source tree that implements
// kernel/saga/journal.Journal must have at least one
// sagajournaltest.RunConformanceSuite call in a _test.go file belonging to its
// package. Packages without such a call are reported as violations.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations: no production saga code uses
//     this pattern; confirmed by
//     TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). If a generated mock appears in a production
//     non-test file, the archtest will flag it — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding journal.Journal):
//     such a type structurally satisfies the interface but provides no real
//     storage. These are rare and only appear in test helpers (which live in
//     _test.go files, excluded from the impl scan). Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (USERREPO-CONFORMANCE-ENROLLMENT-01)
// ref: docs/plans/202605230231-046-saga-l3-workflow-implementation-plan.md §PR-04

const (
	sagaJournalIfacePkg     = "github.com/ghbvf/gocell/kernel/saga/journal"
	sagaJournalIfaceName    = "Journal"
	sagaConformancePkg      = "github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	sagaConformanceFuncName = "RunConformanceSuite"
)

// TestSagaJournalConformanceEnrollment enforces SAGA-JOURNAL-CONFORMANCE-
// ENROLLMENT-01: every concrete type implementing kernel/saga/journal.Journal
// in the production tree must have a sagajournaltest.RunConformanceSuite call
// in a _test.go file of its package (or the corresponding external _test
// variant).
func TestSagaJournalConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve journal.Journal interface ──────────────────────────
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so that types.Implements uses pointer-identical *types.Named
	// descriptors (cross-load comparisons are always false).
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalIfacePkg {
				if obj := p.Pkg.Scope().Lookup(sagaJournalIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
				// The iface package's MemJournal is also an impl; keep this pkg
				// in implPkgs so its concrete types are scanned.
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface,
		"SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01: failed to resolve journal.Journal interface; "+
			"check import path %s", sagaJournalIfacePkg)

	// ─── Step 2: collect all concrete implementations ───────────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // pkg path → true
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
	}

	require.NotEmpty(t, implSet,
		"SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01: zero Journal implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least journal.MemJournal and saga.PGJournal.")

	// ─── Step 3: scan test corpus for RunConformanceSuite call sites with
	// impl-level enrollment. A test file is credited with enrolling impl X
	// only if (a) it contains a sagajournaltest.RunConformanceSuite call AND
	// (b) it constructs X (constructor call whose return type unwraps to X).
	// Package co-location is no longer enough — closes the gap where two
	// impls in one package could share a single conformance call.
	enrolledImpls := make(map[string]bool)

	testPatterns := prodscan.Patterns(root)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasSagaConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					for _, implKey := range extractEnrolledImpls(call, p.TypesInfo, implSet) {
						enrolledImpls[implKey] = true
					}
				})
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: kernel/saga/journal.Journal impl %q not enrolled "+
					"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"both calls sagajournaltest.RunConformanceSuite(t, factory) "+
					"AND constructs %s inside the factory closure (impl-level "+
					"enrollment).",
				implKey, pkgPath, implKey),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestSagaJournalConformanceEnrollment_REDFixture simulates an
// impl-level "missing enrollment" by dropping one impl from the
// enrolled set and asserts the diagnostic logic produces at least
// one violation. The fixture exercises the same comparison logic
// the main test uses (now impl-level keys rather than pkg paths).
func TestSagaJournalConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalIfacePkg {
				if obj := p.Pkg.Scope().Lookup(sagaJournalIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface, "REDFixture: could not resolve Journal interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty")

	// Pick an arbitrary impl as the "missing enrollment" target.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}

	// Build enrolled impls = all impls EXCEPT the target. The diagnostic
	// logic must flag the target.
	enrolledImpls := make(map[string]bool)
	for k := range implSet {
		if k != targetImplKey {
			enrolledImpls[k] = true
		}
	}

	var diags []Diagnostic
	for implKey := range implSet {
		if !enrolledImpls[implKey] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}
	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing impl %q from enrolledImpls must produce ≥1 violation, got 0", targetImplKey)
}

// TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl (B1)
// confirms no production non-test file uses the string literal "Journal" as
// a reflect target that could construct an implicit impl bypassing types.
func TestSagaJournalConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	scope := ModuleScope(root)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Skip test files and the saga packages that legitimately mention
			// "Journal" in docstrings / godoc / interface names.
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if strings.HasPrefix(rel, "kernel/saga/journal/") ||
				strings.HasPrefix(rel, "kernel/saga/sagajournaltest/") ||
				strings.HasPrefix(rel, "adapters/postgres/saga/") ||
				strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if val == sagaJournalIfaceName {
					const b1msg = "blind-spot B1: string literal \"Journal\" in production code " +
						"outside saga packages may indicate reflect-based impl " +
						"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01)"
					out = append(out, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(lit.Pos()).Line,
						Message: b1msg,
					})
				}
			})
		}
		return out
	})
	assert.Empty(t, diags,
		"B1 reverse: no production non-test file outside saga packages should contain string literal %q", sagaJournalIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectSagaJournalImpls adds to implSet all concrete types in pkg (exported
// AND unexported) that implement Journal (value or pointer receiver).
// Interface types are skipped. Unexported impls must also enroll — a package-
// private fake/wrapper that satisfies the interface still risks behavior
// drift if not exercised by the conformance suite.
func collectSagaJournalImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		t := obj.Type()
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// extractEnrolledImpls inspects a CallExpr's callee signature and returns
// implKey strings ("pkg/path.TypeName") for every concrete Journal impl that
// the call constructs (return values whose underlying named type is in
// implSet). Pointer wrappers (*T) are unwrapped. Cross-package type identity
// is irrelevant — we compare by string keys, so the iface-pass and test-pass
// loads do not need to share *types.Named instances.
//
// This is the impl-level upgrade of the prior package-level enrollment: a
// test file is now only credited with enrolling impl X if it actually
// constructs X — package co-location is no longer enough.
func extractEnrolledImpls(call *ast.CallExpr, info *types.Info, implSet map[string]bool) []string {
	if info == nil {
		return nil
	}
	calleeType := info.TypeOf(call.Fun)
	if calleeType == nil {
		return nil
	}
	sig, ok := calleeType.(*types.Signature)
	if !ok {
		return nil
	}
	var out []string
	results := sig.Results()
	for i := 0; i < results.Len(); i++ {
		rt := results.At(i).Type()
		if ptr, isPtr := rt.(*types.Pointer); isPtr {
			rt = ptr.Elem()
		}
		named, ok := rt.(*types.Named)
		if !ok {
			continue
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		key := obj.Pkg().Path() + "." + obj.Name()
		if implSet[key] {
			out = append(out, key)
		}
	}
	return out
}

// hasSagaConformanceCall returns true when file contains at least one call to
// sagajournaltest.RunConformanceSuite resolved via TypesInfo.
func hasSagaConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == sagaConformancePkg && name == sagaConformanceFuncName
	})
	return ok
}

// ============================================================================
// SAGA-JOURNAL-HOLDER-SEAL-01   (from saga_journal_holder_seal_test.go)
// ============================================================================
// INVARIANT: SAGA-JOURNAL-HOLDER-SEAL-01
//
// Three field-shape rules over runtime/saga production structs:
//
//  1. No struct may hold a Heartbeat-bearing journal interface as a persisted
//     field — i.e. neither kernel/saga/journal.Journal (the full interface) nor
//     kernel/saga/journal.Heartbeater. A persisted Heartbeat-capable field is
//     exactly what a centralized heartbeat loop needs (the #1181 anti-pattern);
//     it is forbidden everywhere, Coordinator included.
//  2. kernel/saga/journal.JournalCore (the Heartbeat-free core) may be held only
//     by runtime/saga.Coordinator. Any other holder is a violation.
//  3. No struct may persist a Heartbeater-SHAPED func value as a field
//     (func(context.Context, idutil.SafeID, idutil.SafeID, time.Duration)
//     (bool, error)). A persisted heartbeat func is the func-value equivalent of
//     a journal.Heartbeater field: it lets a centralized loop be reconstructed
//     from a heartbeat func handed in from OUTSIDE runtime/saga (where the
//     `.Heartbeat` selector is beyond SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 A1's
//     scope). Forbidden everywhere, Coordinator included. (rule 3 / F3)
//
// The full journal.Journal still exists transiently as the NewCoordinator
// parameter handed straight to executor.NewExecutor (the sanctioned per-step
// heartbeat funnel); that is a parameter, not a field, so it never trips rule 1.
//
// # Relationship to the JournalCore split (#1209)
//
// Before #1209 this seal tracked the flat journal.Journal interface, and
// Coordinator held journal.Journal directly. #1209 split journal.Journal into
// JournalCore (6 methods) + Heartbeater (Heartbeat) and narrowed
// Coordinator.journal to JournalCore so c.journal.Heartbeat(...) is a compile
// error — that is what upgrades SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01's upstream
// to Hard (see that archtest). This seal is the field-shape complement: it keeps
// the narrow type single-held and bans any re-introduction of a Heartbeat-bearing
// field.
//
// # AI-robust rating
//
// Upstream Medium: a typed-aware go/types scan locks holder-struct identity
// within the package; a new non-Coordinator JournalCore field, any
// Heartbeat-bearing journal interface field, or any Heartbeater-shaped func
// field (rule 3) is rejected at archtest time, not by the compiler.
// Downstream N/A: this is a field-shape invariant, not a callsite invariant —
// there is no caller allowlist.
//
// The JournalCore split does NOT make THIS seal Hard: a non-Coordinator struct
// declaring a JournalCore field is still source-expressible (the archtest, not
// the type system, rejects it). The upstream-Hard path for the holder seal
// (envelope / sealed-construction so a non-Coordinator field is compile-time
// inexpressible) is tracked in gh issue #982; post-#1209 #982 tracks JournalCore
// rather than the full Journal. (Historic note: this godoc previously cited
// #981, which is an unrelated rule — SAGA-STEP-COMPENSATE-PURE-01.)
//
// # Blind-spot self-test (AI-robust §"工具选定后强制盲区自检")
//
// A1 uses go/types field-type resolution; B1 is an AST-only alias-declaration
// scan. Forms outside those tools' reach:
//
//   - B1 (reverse self-test): a `type X = journal.{Journal,JournalCore,Heartbeater}`
//     alias in runtime/saga would let a struct hold `X` evading a pure-AST name
//     match. A1's go/types resolution chases through aliases via types.Unalias
//     (mandatory on Go 1.23+ where an alias is *types.Alias), but B1 catches the
//     alias declaration itself before any struct uses it. Covered.
//   - Embedded (anonymous) field `struct { journal.Heartbeater }`: ast.StructType
//     Fields.List includes embedded fields (Names empty, Type set), so A1
//     classifies it. Covered.
//   - Pointer field `*journal.JournalCore`: classifyResolvedJournalType unwraps
//     one pointer level. Covered. Double pointer `**journal.JournalCore` is a
//     degenerate, non-idiomatic form — accepted residual blind spot.
//   - Cross-package alias `type J = journal.JournalCore` declared OUTSIDE
//     runtime/saga then imported and used as a field type inside runtime/saga:
//     A1 classifies it correctly because classifyResolvedJournalType calls
//     types.Unalias (required on Go 1.23+ where an alias is *types.Alias, not
//     *types.Named). B1 (which only scans runtime/saga) does not flag the foreign
//     declaration, but A1 resolves the field type regardless. Accepted residual
//     for B1; A1-covered.
//   - Local interface that re-declares the Heartbeat shape, e.g.
//     `type beat interface { Heartbeat(...) }` held as a field: a new named type,
//     NOT journal.*, so this seal does not classify it. Accepted residual —
//     composed with SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01, whose A1 callsite scan
//     is signature-shape (not name) based and flags any actual .Heartbeat(...)
//     call in runtime/saga regardless of the holder type. Holding without calling
//     is inert; calling is caught there.
//   - B1 resolves the journal package's local binding from each file's imports
//     (journalPackageLocalNames), so an import alias `import sagajournal "…/journal"`
//     is covered (F2 fix). A dot-import `import . "…/journal"` would make the
//     alias RHS a bare Ident (`type X = JournalCore`, no SelectorExpr) — not
//     matched by B1; accepted residual (dot-imports are non-idiomatic and A1
//     still resolves any field that actually uses such an alias). B1 only scans
//     non-test files in runtime/saga; test files may alias freely.

// journalInterfacePkgPath is the canonical import path of the package that
// declares the Journal / JournalCore / Heartbeater interfaces. Hardcoded rather
// than derived from go.mod because it is load-bearing: a rename of this package
// path would be a contract break requiring a deliberate update here.
const journalInterfacePkgPath = "github.com/ghbvf/gocell/kernel/saga/journal"

// journalInterfaceTypeName / heartbeaterInterfaceTypeName name the two
// Heartbeat-bearing interfaces that may not be persisted as a field anywhere in
// runtime/saga production code (rule 1).
const (
	journalInterfaceTypeName     = "Journal"
	heartbeaterInterfaceTypeName = "Heartbeater"
)

// journalCoreInterfaceTypeName names the Heartbeat-free core interface that only
// the Coordinator may hold as a field (rule 2).
const journalCoreInterfaceTypeName = "JournalCore"

// allowedSagaJournalHolder is the single struct in runtime/saga permitted to
// hold a journal.JournalCore field.
const allowedSagaJournalHolder = "Coordinator"

// sagaJournalHolderSealRule is the rule ID prefixed to every diagnostic message.
const sagaJournalHolderSealRule = "SAGA-JOURNAL-HOLDER-SEAL-01"

// journalFieldKind classifies a struct field's resolved type against the three
// journal-package interfaces this seal cares about.
type journalFieldKind int

const (
	journalFieldNone        journalFieldKind = iota // not a journal-package interface
	journalFieldFull                                // journal.Journal (Heartbeat-bearing)
	journalFieldHeartbeater                         // journal.Heartbeater (Heartbeat-bearing)
	journalFieldCore                                // journal.JournalCore (Heartbeat-free)
)

// classifyJournalFieldType resolves the field expression via go/types and
// classifies it. Returns journalFieldNone when TypesInfo is nil or the type is
// not one of the three journal-package interfaces.
func classifyJournalFieldType(info *types.Info, expr ast.Expr) journalFieldKind {
	if info == nil {
		return journalFieldNone
	}
	tv, ok := info.Types[expr]
	if !ok {
		return journalFieldNone
	}
	return classifyResolvedJournalType(tv.Type)
}

// classifyResolvedJournalType checks whether t (possibly wrapped in one pointer
// or resolved through a type alias) is journal.Journal, journal.Heartbeater, or
// journal.JournalCore.
//
// On Go 1.23+ (gotypesalias=1, the default — this module is on go 1.25), a type
// alias materializes as *types.Alias, NOT transparently as the aliased
// *types.Named. A bare t.(*types.Named) assertion therefore MISSES alias-typed
// fields (a field of `type J = journal.JournalCore` resolves to *types.Alias and
// the assertion fails). types.Unalias collapses an alias to its underlying type
// so the Obj().Pkg().Path()/Name() check below is canonical regardless of how
// many alias / pointer layers wrap the field type (go/types.Unalias expands a
// type to the one it denotes after resolving package-level aliases).
func classifyResolvedJournalType(t types.Type) journalFieldKind {
	if t == nil {
		return journalFieldNone
	}
	// Collapse a top-level alias (`type J = journal.JournalCore`) before the
	// pointer probe, then again after unwrapping a pointer (`*J`, or
	// `type J = *journal.JournalCore`), so every alias⇄pointer ordering resolves.
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return journalFieldNone
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != journalInterfacePkgPath {
		return journalFieldNone
	}
	switch obj.Name() {
	case journalInterfaceTypeName:
		return journalFieldFull
	case heartbeaterInterfaceTypeName:
		return journalFieldHeartbeater
	case journalCoreInterfaceTypeName:
		return journalFieldCore
	}
	return journalFieldNone
}

// journalFieldSealDiag applies the two seal rules to a single struct field and
// returns a diagnostic when violated.
func journalFieldSealDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	kind := classifyJournalFieldType(p.TypesInfo, field.Type)
	if kind == journalFieldNone {
		return Diagnostic{}, false
	}
	// Rule 2: JournalCore is allowed, but only on the Coordinator.
	if kind == journalFieldCore && holderName == allowedSagaJournalHolder {
		return Diagnostic{}, false
	}
	pos := p.Fset.Position(field.Pos())
	return Diagnostic{Rel: rel, Line: pos.Line, Message: journalFieldSealMessage(kind, holderName)}, true
}

// journalFieldSealMessage renders the diagnostic text for a violating field.
func journalFieldSealMessage(kind journalFieldKind, holderName string) string {
	if kind == journalFieldCore {
		return fmt.Sprintf(
			"%s: struct %q holds a journal.JournalCore field; only %q may hold it",
			sagaJournalHolderSealRule, holderName, allowedSagaJournalHolder)
	}
	// journalFieldFull / journalFieldHeartbeater — a Heartbeat-bearing field.
	return fmt.Sprintf(
		"%s: struct %q holds a Heartbeat-bearing journal interface field "+
			"(journal.Journal or journal.Heartbeater); no struct in runtime/saga may "+
			"persist a Heartbeat-capable field — hold journal.JournalCore instead. The "+
			"full Journal exists transiently only as the NewCoordinator parameter handed "+
			"to executor.NewExecutor (the sanctioned per-step heartbeat funnel).",
		sagaJournalHolderSealRule, holderName)
}

// heartbeatFuncFieldDiag (rule 3) flags a struct field whose type is a func —
// anonymous, named, aliased, or pointer-to-func — matching the Heartbeater
// signature shape. Persisting such a callable is the func-value equivalent of
// holding a journal.Heartbeater field: a centralized heartbeat loop can be
// reconstructed from a heartbeat func passed into the constructor from OUTSIDE
// runtime/saga, where the `.Heartbeat` selector is beyond
// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 A1's scope. Closing the field-
// persistence path here blocks the loop (a loop needs a persisted callable; a
// bare constructor closure capturing the param is the irreducible residual, same
// class as that archtest's NewCoordinator pass-through window). No struct in
// runtime/saga — Coordinator included — may persist a heartbeat-shaped func.
func heartbeatFuncFieldDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	if p.TypesInfo == nil {
		return Diagnostic{}, false
	}
	tv, ok := p.TypesInfo.Types[field.Type]
	if !ok {
		return Diagnostic{}, false
	}
	// Collapse alias + one pointer level (mirrors classifyResolvedJournalType),
	// then require the underlying type to be a func signature of the heartbeat
	// shape. Interface fields (Underlying = *types.Interface) never match here —
	// they are journal-package interfaces handled by classifyJournalFieldType.
	t := types.Unalias(tv.Type)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	sig, ok := t.Underlying().(*types.Signature)
	if !ok || !signatureMatchesHeartbeaterShape(sig) {
		return Diagnostic{}, false
	}
	pos := p.Fset.Position(field.Pos())
	return Diagnostic{
		Rel:  rel,
		Line: pos.Line,
		Message: fmt.Sprintf(
			"%s: struct %q holds a Heartbeater-shaped func field "+
				"(func(context.Context, idutil.SafeID, idutil.SafeID, time.Duration) (bool, error)); "+
				"no struct in runtime/saga may persist a Heartbeat-capable callable (interface OR func) "+
				"— a persisted heartbeat func reconstructs the centralized-loop anti-pattern from a value "+
				"passed in from outside runtime/saga. Funnel per-step heartbeat through executor.",
			sagaJournalHolderSealRule, holderName),
	}, true
}

// TestSagaJournalHolderSeal_A1_OnlyCoordinatorHoldsJournal scans runtime/saga
// production source for struct fields whose resolved type is a journal-package
// interface, applying both seal rules (see package godoc):
//   - journal.Journal / journal.Heartbeater field anywhere → violation
//   - journal.JournalCore field outside Coordinator → violation
//
// Uses RunTyped (not Run) so go/types can resolve field types across package
// boundaries — a pure AST scan cannot distinguish `journal.JournalCore` from any
// other selector named "JournalCore" without type information.
func TestSagaJournalHolderSeal_A1_OnlyCoordinatorHoldsJournal(t *testing.T) {
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
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}

			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return
				}
				holderName := ts.Name.Name
				for _, field := range st.Fields.List {
					if d, ok := journalFieldSealDiag(p, rel, holderName, field); ok {
						out = append(out, d)
						continue
					}
					if d, ok := heartbeatFuncFieldDiag(p, rel, holderName, field); ok {
						out = append(out, d)
					}
				}
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-A1", diags)
}

// journalPackageLocalNames returns the set of local identifiers in file bound
// to the journal interface package (journalInterfacePkgPath): the default
// package name for a plain import, plus any explicit import alias
// (`import sagajournal "…/journal"`). Blank (`_`) and dot (`.`) imports are not
// usable as a `pkg.Type` selector base and are excluded — a dot-imported
// `type X = JournalCore` is a bare-Ident form (no SelectorExpr) noted as a
// residual in the package godoc.
func journalPackageLocalNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != journalInterfacePkgPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names[path.Base(journalInterfacePkgPath)] = true // default name: "journal"
		case imp.Name.Name == "_" || imp.Name.Name == ".":
			// not usable as a selector base; skip
		default:
			names[imp.Name.Name] = true
		}
	}
	return names
}

// journalInterfaceAliasName reports the journal interface name aliased by ts if
// ts is `type X = <localName>.{Journal,JournalCore,Heartbeater}` where localName
// is any local binding of the journal package (journalLocalNames), else
// ("", false). Resolving via journalLocalNames rather than a hardcoded "journal"
// closes the import-alias evasion: `import sagajournal "…/journal"` followed by
// `type X = sagajournal.JournalCore` is now flagged.
func journalInterfaceAliasName(ts *ast.TypeSpec, journalLocalNames map[string]bool) (string, bool) {
	// An alias has a valid Assign token.
	if !ts.Assign.IsValid() {
		return "", false
	}
	sel, ok := ts.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || !journalLocalNames[id.Name] {
		return "", false
	}
	switch sel.Sel.Name {
	case journalInterfaceTypeName, journalCoreInterfaceTypeName, heartbeaterInterfaceTypeName:
		return sel.Sel.Name, true
	}
	return "", false
}

// TestSagaJournalHolderSeal_BlindSpot_B1_NoAliasInRuntimeSaga ensures production
// non-test files in runtime/saga do not introduce a type alias of the form
// `type X = journal.Journal` / `journal.JournalCore` / `journal.Heartbeater`.
// Such an alias would let a new struct declare a field of type X — structurally
// identical to the aliased interface — while evading A1's exact-name check (if A1
// were a pure AST string match rather than a types-resolved check). A1 uses
// go/types resolution (which chases through aliases), so B1 is defense-in-depth:
// it catches the alias declaration itself before any struct can use it.
//
// B1 uses Run (AST-only) because detecting the alias is a pure syntactic check:
// an alias TypeSpec has a valid ts.Assign token and the RHS is a SelectorExpr
// whose X.Name binds to the journal package. The binding is resolved from the
// file's own import specs (journalPackageLocalNames), so an import alias
// (`import sagajournal "…/journal"`) is covered without cross-package type
// resolution at the declaration site.
func TestSagaJournalHolderSeal_BlindSpot_B1_NoAliasInRuntimeSaga(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			journalNames := journalPackageLocalNames(file)
			if len(journalNames) == 0 {
				continue // file does not import the journal package
			}

			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				aliased, ok := journalInterfaceAliasName(ts, journalNames)
				if !ok {
					return
				}
				pos := p.Fset.Position(ts.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s-B1: type alias %q = journal.%s in runtime/saga; remove the alias "+
							"and reference the journal interface directly — aliases create an "+
							"evasion path for the holder-seal invariant",
						sagaJournalHolderSealRule, ts.Name.Name, aliased),
				})
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-B1", diags)
}

// TestSagaJournalHolderSeal_BlindSpot_B1_MatcherNonVacuous proves the B1
// alias-matcher is wired and non-vacuous. B1's production scan reports zero
// violations today (by design), so — unlike NO-HEARTBEAT-LOOP's B1, which can
// assert "executor has >=1 callsite" — it cannot demonstrate non-vacuity from
// production source. Instead this exercises journalInterfaceAliasName against a
// synthetic AST covering every form: the three sealed alias names must match,
// and non-aliases / wrong package / non-selector / definition (non-alias) forms
// must NOT. If the matcher silently stopped firing, B1 would pass vacuously and
// this test catches it.
//
// Case H (`sagajournal.JournalCore`) locks the F2 fix: the journal package
// imported under a non-default local name must still match, while case E
// (`other.Journal`, a name NOT bound to the journal package) must not.
func TestSagaJournalHolderSeal_BlindSpot_B1_MatcherNonVacuous(t *testing.T) {
	t.Parallel()

	const src = `package x
type A = journal.Journal
type B = journal.JournalCore
type C = journal.Heartbeater
type D = journal.Other
type E = other.Journal
type F journal.Journal
type G = SomethingElse
type H = sagajournal.JournalCore
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}

	// journalNames models a file that imports the journal package both plainly
	// (local name "journal") and under an alias (`import sagajournal "…/journal"`).
	// "other" is deliberately absent — it is NOT a binding of the journal pkg.
	journalNames := map[string]bool{"journal": true, "sagajournal": true}

	// typeName -> expected aliased journal interface name ("" = must NOT match).
	want := map[string]string{
		"A": journalInterfaceTypeName,     // type X = journal.Journal
		"B": journalCoreInterfaceTypeName, // type X = journal.JournalCore
		"C": heartbeaterInterfaceTypeName, // type X = journal.Heartbeater
		"D": "",                           // journal.Other — not a sealed name
		"E": "",                           // other.Journal — name not bound to journal pkg
		"F": "",                           // definition, not an alias (no '=')
		"G": "",                           // not a selector expression
		"H": journalCoreInterfaceTypeName, // type X = sagajournal.JournalCore (import alias)
	}

	seen := map[string]bool{}
	EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.TYPE {
			return
		}
		EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
			exp, tracked := want[ts.Name.Name]
			if !tracked {
				return
			}
			seen[ts.Name.Name] = true
			aliased, matched := journalInterfaceAliasName(ts, journalNames)
			if exp == "" {
				if matched {
					t.Errorf("journalInterfaceAliasName(%s) = (%q, true); want no match",
						ts.Name.Name, aliased)
				}
				return
			}
			if !matched || aliased != exp {
				t.Errorf("journalInterfaceAliasName(%s) = (%q, %v); want (%q, true)",
					ts.Name.Name, aliased, matched, exp)
			}
		})
	})

	// Non-vacuity: every positive case (incl. the import-aliased H) must have
	// been exercised — otherwise the fixture or the parse silently skipped them.
	for _, name := range []string{"A", "B", "C", "H"} {
		if !seen[name] {
			t.Errorf("synthetic fixture did not exercise positive case %q — "+
				"B1 matcher self-test is vacuous", name)
		}
	}
}

// TestSagaJournalHolderSeal_A1_AliasFieldResolvedViaUnalias is the F1 regression:
// classifyResolvedJournalType must resolve a struct field whose type is a
// package-level alias to journal.JournalCore. On Go 1.23+ (gotypesalias=1, this
// module is on go 1.25) the field type is *types.Alias; without types.Unalias
// the *types.Named assertion fails and the alias-typed holder silently evades
// the seal — defeating the holder seal with a one-line alias.
//
// The fixture (testdata/saga_journal_alias_fixtures/aliasholder) has two
// non-Coordinator holders — one direct, one via alias — and BOTH must be flagged.
// Pre-fix the alias holder is missed (this test fails); post-fix both surface.
func TestSagaJournalHolderSeal_A1_AliasFieldResolvedViaUnalias(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t, FixtureOpts{},
		[]string{"./tools/archtest/testdata/saga_journal_holder_seal_fixtures/aliasholder"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := filepath.ToSlash(p.Rel(file))
				EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					// holderName is never "Coordinator" for fixture structs, so
					// the rule-2 JournalCore allowance never applies — every
					// JournalCore field (direct or aliased) must surface.
					for _, field := range st.Fields.List {
						if d, ok := journalFieldSealDiag(p, rel, ts.Name.Name, field); ok {
							out = append(out, d)
						}
					}
				})
			}
			return out
		})

	var directFlagged, aliasFlagged bool
	for _, d := range diags {
		if strings.Contains(d.Message, "HolderDirect") {
			directFlagged = true
		}
		if strings.Contains(d.Message, "HolderViaAlias") {
			aliasFlagged = true
		}
	}
	if !directFlagged {
		t.Errorf("%s-A1: fixture HolderDirect (journal.JournalCore field) not flagged — "+
			"the holder-seal classifier is broken (fixture wiring or scan logic)",
			sagaJournalHolderSealRule)
	}
	if !aliasFlagged {
		t.Errorf("%s-A1: fixture HolderViaAlias (alias to journal.JournalCore) not flagged — "+
			"classifyResolvedJournalType is not calling types.Unalias; on Go 1.23+ an alias "+
			"is *types.Alias, so the alias-typed field evades the seal (F1 regression)",
			sagaJournalHolderSealRule)
	}
}

// TestSagaJournalHolderSeal_A1_HeartbeatFuncFieldFlagged is the F3 regression
// (rule 3): a struct persisting a Heartbeater-shaped func value as a field — the
// func-value equivalent of a journal.Heartbeater field — must be flagged, while
// a non-heartbeat func field must NOT (shape-specific, not "any func"). This
// closes the path where a heartbeat func handed in from outside runtime/saga is
// stashed in a field to drive a centralized loop, which neither the interface
// holder seal nor the name-filtered NO-HEARTBEAT-LOOP A1 catches.
func TestSagaJournalHolderSeal_A1_HeartbeatFuncFieldFlagged(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t, FixtureOpts{},
		[]string{"./tools/archtest/testdata/saga_journal_holder_seal_fixtures/funcfieldholder"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := filepath.ToSlash(p.Rel(file))
				EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					for _, field := range st.Fields.List {
						if d, ok := heartbeatFuncFieldDiag(p, rel, ts.Name.Name, field); ok {
							out = append(out, d)
						}
					}
				})
			}
			return out
		})

	var anonFlagged, namedFlagged, plainFlagged bool
	for _, d := range diags {
		switch {
		case strings.Contains(d.Message, "HeartbeatFuncHolder"):
			anonFlagged = true
		case strings.Contains(d.Message, "NamedFuncHolder"):
			namedFlagged = true
		case strings.Contains(d.Message, "PlainFuncHolder"):
			plainFlagged = true
		}
	}
	if !anonFlagged {
		t.Errorf("%s-A1: HeartbeatFuncHolder (anonymous heartbeat-shaped func field) not flagged — "+
			"the func-value evasion path is open (F3 regression)", sagaJournalHolderSealRule)
	}
	if !namedFlagged {
		t.Errorf("%s-A1: NamedFuncHolder (named heartbeat-shaped func type field) not flagged — "+
			"heartbeatFuncFieldDiag must resolve the named type's underlying signature", sagaJournalHolderSealRule)
	}
	if plainFlagged {
		t.Errorf("%s-A1: PlainFuncHolder (non-heartbeat func field) wrongly flagged — "+
			"the shape match is too loose (would false-positive on ordinary func fields)", sagaJournalHolderSealRule)
	}
}

// ============================================================================
// SAGA-DRIVE-BEHIND-LEADER-GATE-01   (from saga_leader_gate_test.go)
// ============================================================================
// INVARIANT: SAGA-DRIVE-BEHIND-LEADER-GATE-01
//
// SAGA-DRIVE-BEHIND-LEADER-GATE-01 — leader-elect gate call-discipline lock for the
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
// ref: SAGA-STEP-RUN-OUTSIDE-TX-01 section (callsite-discipline pattern)
// ref: .claude/rules/gocell/ai-robust.md §"Funnel 双向锁评级"

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
		if _, ok := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
			return callIsMethodNamed(call, acquireLeadMethodName)
		}); ok {
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
	as, found := FindFirstInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) bool {
		if len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			return false
		}
		call, isCall := as.Rhs[0].(*ast.CallExpr)
		if !isCall || !callIsMethodNamed(call, acquireLeadMethodName) {
			return false
		}
		id, isID := as.Lhs[1].(*ast.Ident)
		return isID && id.Name != "_"
	})
	if !found {
		return "", false
	}
	id := as.Lhs[1].(*ast.Ident)
	return id.Name, true
}

// leadGatesDrive reports whether some IfStmt in body has a condition referencing
// leadVar and a body that either early-exits (BranchStmt/ReturnStmt) or contains
// a driveOne call — the two sanctioned gate shapes.
func leadGatesDrive(body *ast.BlockStmt, leadVar string) bool {
	_, ok := FindFirstInSubtree[ast.IfStmt](body, func(ifs *ast.IfStmt) bool {
		if ifs.Cond == nil || !condReferences(ifs.Cond, leadVar) {
			return false
		}
		return ifBodyEarlyExits(ifs.Body) || bodyCallsMethodNamed(ifs.Body, driveOneMethodName)
	})
	return ok
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

// ============================================================================
// SAGA-STATUS-FANOUT-COVERAGE-01   (from saga_status_fanout_coverage_test.go)
// ============================================================================
// INVARIANT: SAGA-STATUS-FANOUT-COVERAGE-01
//
// SAGA-STATUS-FANOUT-COVERAGE-01 — enforces the contract-fanout closure for
// the saga.Status / journal.EventKind enums: when a constant is added, its fanout
// carriers must stay in lockstep, or the build / CI goes red.
//
// Motivation: PR #1210 C6 added saga.StatusCompensationFailed (=8) plus
// journal.KindStepCompensationFailed (=10) / KindSagaCompensationFailed (=11),
// but every fanout carrier (the readyz status table, the conformance terminal
// coverage, the alerting kind legend, the terminal→kind mapping) silently
// drifted and was only fixed after N rounds of review. .claude/rules/gocell/
// contract-fanout.md mandates the fanout; this is its machine guard.
//
// # Mechanism (codegen funnel + compile gate — Hard)
//
// The single source of truth is the saga.Status / journal.EventKind const set.
// `gocell generate saga-coverage` (tools/codegen/sagacoveragegen) renders three
// byte-locked artifacts from it:
//
//   - kernel/saga/sagajournaltest/terminal_coverage_gen.go — a struct with
//     exactly one field per TERMINAL saga.Status. conformance.go populates it
//     with a KEYLESS composite literal (terminalHappyPaths), so adding a terminal
//     status const → regen adds a field → the keyless literal fails to compile
//     ("too few values in struct literal") until a happy-path driver is supplied.
//     This is a COMPILE-TIME exhaustiveness gate — the Hard half. The conformance
//     harness (runTerminalCoverage) then runs each driver and asserts the
//     happy-path outcome (got==want, ok, terminal kind, lease released), so a
//     negative or mis-mapped driver is caught at runtime.
//   - the readyz.md saga lifecycle status table (Status / Value / Phase /
//     Terminal? rows).
//   - the alerting-rules.md "kind 速查" legend (value=wire entries).
//
// # Sub-rule index
//
//   - GOLDEN: each committed artifact (the gen file + the two marker-delimited
//     doc regions) is byte-identical to a fresh sagacoveragegen.Render(). A drift
//     surfaces as a red test naming the file and the regen command. This single
//     check subsumes the former C1 (conformance coverage), C2 (readyz table
//     forward/reverse + Value/Terminal? columns) and C3 (alerting legend) — none
//     can drift from the const set without the golden lock firing.
//   - C4: journal.TerminalEventKind switch exhaustiveness — its case set equals
//     the Status.IsTerminal() terminal set (no missing, no extra). TerminalEventKind
//     is production logic (event.go), not a generated artifact, so it keeps a
//     dedicated type-aware archtest. The runtime harness's got-kind assertion is a
//     second line of defense for the "missing case" direction; C4 also catches the
//     "extra case" direction.
//   - C5 (const-set ⇄ Valid()-range bijection): the go/types declared const set
//     of saga.Status / journal.EventKind MUST equal the value set Render()
//     enumerates via `for v := <start>; v.Valid(); v++`. This is what makes the
//     "const set is the single source of truth" claim STRICT: Render() (and hence
//     the golden) enumerates by the Valid()-loop, so without C5 a const added
//     without extending Valid() would be silently invisible to the whole funnel.
//     C5 enumerates the const set independently (go/types, compiler-derived) and
//     fails if it diverges from the loop in either direction. Type-aware archtest
//     (Medium) — closes blind-spot B2 below.
//
// # Blind-spot catalog (forms the chosen tools cannot see) + reverse self-checks
//
//   - B1 (switch-form coupling): C4 parses the case clauses of
//     journal.TerminalEventKind and Status.IsTerminal(). If either is refactored
//     away from a switch (map lookup, slices.Contains), sfcCollectSwitchStatusCases
//     yields an empty set. NOT a vacuous pass: the require.NotEmpty floor guards in
//     TestSagaStatusFanoutCoverageC4 fire and name both root causes (switch-form
//     change OR Status rename).
//   - B2 (const-set ⇄ Valid()-range coupling — NOW MACHINE-CHECKED BY C5): the
//     generator enumerates via `for v := <start>; v.Valid(); v++`. A const added
//     without extending Valid() (the loop stops before it), or a Valid() range that
//     exceeds / is non-contiguous with the declared const set, would make Render()
//     and the golden lock blind to part of the const set. Formerly dismissed as "a
//     self-contradiction the const author would not create"; that dismissal is
//     retired — C5 (TestSagaStatusFanoutCoverageC5) enumerates the const set
//     independently via go/types and fails on any divergence from the loop, in
//     either direction. C5's own residual blind spots are compile-gated: the loop
//     start sentinels (saga.StatusPending / journal.KindStepStarted) are referenced
//     by name, so renaming/removing them breaks the archtest build; renaming Valid()
//     drops the method-location floor guard (require.NotZero) rather than passing
//     vacuously.
//   - RED-fixtures: TestSagaStatusFanoutCoverageC4_REDFixture exercises
//     sfcDiagsTerminalEventKind on synthetic mismatched sets;
//     TestSagaStatusFanoutCoverageC5_REDFixture exercises sfcDiagsConstSetValidRange
//     on synthetic const-set/loop divergence (including the user-reported
//     "added a const but forgot Valid()" scenario); TestSagaCoverageGolden_REDFixture
//     exercises sfcGoldenDiags on synthetic drifted artifacts; all prove the live
//     checks emit diagnostics on drift even though they are green on aligned source.
//     TestSagaCoverageDiagnosticLocations asserts every emitted Diagnostic (C4, C5,
//     and golden) carries a real module-relative Rel and a non-zero Line (no
//     import-path Rel, no :0:).
//
// # AI-robust grading: Hard (codegen funnel + type-system compile gate)
//
// SUPERSEDES the prior "permanent Medium ceiling / Hard infeasible" grade. That
// grade conflated two codegen routes: codegen-ing the conformance DRIVE bodies
// (genuinely defeated by the non-mechanical legal-source-phase setup) versus
// codegen-ing the exhaustiveness SKELETON (NOT defeated). Go's keyless struct
// literal is a compile-time exhaustiveness primitive: a generated
// field-per-terminal struct + a hand-written keyless literal makes "added a
// terminal without coverage" a compile error. The drives stay hand-written; only
// the coverage skeleton + the doc fanout fragments are generated and golden-locked.
//
//   - Downstream Hard: the keyless literal in conformance.go cannot omit a
//     terminal once the generated struct gains its field (compile error). The doc
//     regions and the gen file cannot drift from the const set without the golden
//     lock firing.
//   - Upstream Hard: the generated artifacts are regenerate-and-diff byte-locked
//     against sagacoveragegen.Render(), which derives solely from the
//     saga.Status / journal.EventKind type sets (a rename breaks the generator
//     build). There is no hand-maintained golden list.
//
// ref: tools/codegen/sagacoveragegen (the single-source generator)
// ref: tools/codegen/requireddepsgen + tools/archtest/required_dep_nil_guard_test.go (generator-golden pattern)
// ref: .claude/rules/gocell/contract-fanout.md (the fanout obligation this guards)

const (
	sfcStatusPkgPath  = "github.com/ghbvf/gocell/kernel/saga"
	sfcJournalPkgPath = "github.com/ghbvf/gocell/kernel/saga/journal"
	sfcStatusTypeName = "Status"
	sfcKindTypeName   = "EventKind"
	sfcReadyzDocRel   = "docs/ops/readyz.md"
	sfcAlertingDocRel = "docs/ops/alerting-rules.md"
	sfcGenFileRel     = "kernel/saga/sagajournaltest/terminal_coverage_gen.go"
)

// ─── type-aware helpers (C4) ────────────────────────────────────────────────

// sfcIsTypedConst reports whether obj is a *types.Const whose named type is
// pkgPath.typeName.
func sfcIsTypedConst(obj types.Object, pkgPath, typeName string) bool {
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	named, ok := c.Type().(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil && tobj.Pkg().Path() == pkgPath && tobj.Name() == typeName
}

// sfcReceiverIsType reports whether fd's receiver resolves to pkgPath.typeName
// (value or pointer receiver).
func sfcReceiverIsType(fd *ast.FuncDecl, info *types.Info, pkgPath, typeName string) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	t := info.TypeOf(fd.Recv.List[0].Type)
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	o := named.Obj()
	return o.Pkg() != nil && o.Pkg().Path() == pkgPath && o.Name() == typeName
}

// sfcConstName resolves an ident / selector expression to the name of a const of
// type pkgPath.typeName.
func sfcConstName(expr ast.Expr, info *types.Info, pkgPath, typeName string) (string, bool) {
	var ident *ast.Ident
	switch e := expr.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return "", false
	}
	obj, ok := info.Uses[ident]
	if !ok || !sfcIsTypedConst(obj, pkgPath, typeName) {
		return "", false
	}
	return obj.Name(), true
}

// sfcCollectSwitchStatusCases parses a func/method named fnName (receiver
// matching sfcStatusPkgPath.recvType, or recvType=="" for a package func) and
// returns the set of saga.Status const names appearing in its non-default case
// clauses, plus the func's module-relative file and 1-based line for diagnostics.
func sfcCollectSwitchStatusCases(p *Pass, fnName, recvType string) (set map[string]bool, rel string, line int) {
	set = map[string]bool{}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name.Name != fnName {
				return
			}
			if recvType == "" {
				if fd.Recv != nil {
					return
				}
			} else if !sfcReceiverIsType(fd, p.TypesInfo, sfcStatusPkgPath, recvType) {
				return
			}
			rel = p.Rel(f)
			line = p.Fset.Position(fd.Pos()).Line
			EachInSubtree[ast.CaseClause](fd, func(cc *ast.CaseClause) {
				for _, e := range cc.List {
					if name, ok := sfcConstName(e, p.TypesInfo, sfcStatusPkgPath, sfcStatusTypeName); ok {
						set[name] = true
					}
				}
			})
		})
	}
	return set, rel, line
}

// sfcDiagsTerminalEventKind builds C4 diagnostics: the journal.TerminalEventKind
// switch case set must equal the Status.IsTerminal() terminal set (both
// directions). All diagnostics point at the TerminalEventKind decl (rel:line).
func sfcDiagsTerminalEventKind(isTerminal, tekCases map[string]bool, rel string, line int) []Diagnostic {
	var diags []Diagnostic
	for name := range isTerminal {
		if !tekCases[name] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"terminal saga.%s missing a case in journal.TerminalEventKind switch", name)})
		}
	}
	for name := range tekCases {
		if !isTerminal[name] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"journal.TerminalEventKind has case saga.%s which Status.IsTerminal() does not classify terminal", name)})
		}
	}
	return diags
}

// ─── const-set ⇄ Valid()-range cross-check (C5) ──────────────────────────────

// sfcCollectDeclaredConsts enumerates, via go/types, the integer values of every
// package-scope const whose named type is pkgPath.typeName, and locates the
// type's Valid() method — the site to fix when the const set and the Valid()
// range diverge. Returns the value→constName map plus the Valid() decl's
// module-relative file and 1-based line (for C5 diagnostics).
func sfcCollectDeclaredConsts(p *Pass, pkgPath, typeName string) (values map[int64]string, validRel string, validLine int) {
	values = map[int64]string{}
	if p.Pkg == nil || p.TypesInfo == nil {
		return values, "", 0
	}
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !sfcIsTypedConst(obj, pkgPath, typeName) {
			continue
		}
		v, exact := constant.Int64Val(obj.(*types.Const).Val())
		if !exact {
			continue
		}
		values[v] = name
	}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name.Name != "Valid" || !sfcReceiverIsType(fd, p.TypesInfo, pkgPath, typeName) {
				return
			}
			validRel = p.Rel(f)
			validLine = p.Fset.Position(fd.Pos()).Line
		})
	}
	return values, validRel, validLine
}

// sfcStatusLoopValues returns the saga.Status value set enumerated EXACTLY as
// sagacoveragegen.collectStatuses does — `for s := StatusPending; s.Valid(); s++`
// — so C5 compares the declared const set against the values Render() actually
// sees, not a re-derived range (which would miss a non-contiguous Valid() that
// truncates the loop early). The iteration is capped at the uint8 domain: a
// Valid() that admits the whole domain never terminates the loop, which is
// itself the kind of bug C5 exists to surface, so the cap fails loudly rather
// than hanging CI.
func sfcStatusLoopValues(t *testing.T) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	n := 0
	for s := saga.StatusPending; s.Valid(); s++ {
		out[int64(s)] = true
		if n++; n > 256 {
			t.Fatalf("saga.Status.Valid() admits >256 values — Valid() never terminates the enumeration loop")
		}
	}
	return out
}

// sfcKindLoopValues mirrors sfcStatusLoopValues for journal.EventKind
// (sagacoveragegen.collectKinds enumeration).
func sfcKindLoopValues(t *testing.T) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	n := 0
	for k := journal.KindStepStarted; k.Valid(); k++ {
		out[int64(k)] = true
		if n++; n > 256 {
			t.Fatalf("journal.EventKind.Valid() admits >256 values — Valid() never terminates the enumeration loop")
		}
	}
	return out
}

// sfcDiagsConstSetValidRange builds C5 diagnostics: the go/types declared const
// value set MUST equal the value set Render() enumerates via
// `for v := <start>; v.Valid(); v++`. Any divergence means Render() (and thus
// the fanout golden) cannot see the full declared const set — the gap where a
// new const is added but Valid() is not extended. Both directions are reported;
// every diagnostic points at the type's Valid() method (the fix site).
func sfcDiagsConstSetValidRange(typeLabel string, declared map[int64]string, loop map[int64]bool, rel string, line int) []Diagnostic {
	var diags []Diagnostic
	for v, name := range declared {
		if !loop[v] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"%s const %s (value %d) is declared but %s.Valid() excludes it from the `for v := …; v.Valid(); v++` enumeration — "+
					"Render() and the fanout golden cannot cover it; extend Valid() (and IsTerminal()/String()/the fanout carriers) to admit it",
				typeLabel, name, v, typeLabel)})
		}
	}
	for v := range loop {
		if _, ok := declared[v]; !ok {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"%s.Valid() admits value %d which no declared const carries — Valid()'s range exceeds the "+
					"const set; tighten Valid() or declare the missing const",
				typeLabel, v)})
		}
	}
	return diags
}

// ─── golden lock (GOLDEN) ─────────────────────────────────────────────────────

// sfcGoldenDiags compares the committed fanout artifacts against a fresh
// Render(). It is the pure detection core shared by the live golden test and the
// RED fixture. Every diagnostic carries a real module-relative Rel and non-zero
// Line (F3).
func sfcGoldenDiags(art sagacoveragegen.Artifacts, gen, readyz, alerting []byte) []Diagnostic {
	var diags []Diagnostic

	if !bytes.Equal(gen, art.TerminalCoverageGo) {
		diags = append(diags, Diagnostic{Rel: sfcGenFileRel, Line: 1, Message: sfcRegenMsg(
			"terminal_coverage_gen.go drifted from the saga.Status const set")})
	}

	diags = append(diags, sfcRegionDiag(readyz, sfcReadyzDocRel, art.ReadyzTable,
		sagacoveragegen.ReadyzTableStartMarker, sagacoveragegen.ReadyzTableEndMarker,
		"readyz.md saga status table region")...)
	diags = append(diags, sfcRegionDiag(alerting, sfcAlertingDocRel, art.KindLegend,
		sagacoveragegen.KindLegendStartMarker, sagacoveragegen.KindLegendEndMarker,
		"alerting-rules.md kind legend region")...)

	return diags
}

// sfcRegionDiag compares one marker-delimited doc region against want, pointing
// at the start-marker line on drift (or line 1 if the marker is missing).
func sfcRegionDiag(content []byte, rel, want, start, end, label string) []Diagnostic {
	region, err := sagacoveragegen.ExtractRegion(string(content), start, end)
	if err != nil {
		return []Diagnostic{{Rel: rel, Line: 1, Message: fmt.Sprintf("%s markers not found: %v", label, err)}}
	}
	if region != want {
		return []Diagnostic{{Rel: rel, Line: sfcMarkerLine(content, start), Message: sfcRegenMsg(label + " drifted from source")}}
	}
	return nil
}

func sfcRegenMsg(what string) string {
	return what + " — run `gocell generate saga-coverage` and commit the result"
}

// sfcMarkerLine returns the 1-based line of marker in content (1 if absent).
func sfcMarkerLine(content []byte, marker string) int {
	idx := bytes.Index(content, []byte(marker))
	if idx < 0 {
		return 1
	}
	return bytes.Count(content[:idx], []byte("\n")) + 1
}

// ─── file readers (sanctioned content reader; no os.ReadFile) ─────────────────

func sfcReadFile(t *testing.T, root, dir, rel, ext string) []byte {
	t.Helper()
	sc := DirsScope(root, []string{dir}, MatchRels(func(r string) bool { return r == rel }))
	files, err := LoadContentFiles(sc, []string{ext})
	require.NoError(t, err, "load %s", rel)
	require.Len(t, files, 1, "expected exactly one file at %s", rel)
	return files[0].Bytes
}

func sfcLoadDocs(t *testing.T, root string) map[string][]byte {
	t.Helper()
	sc := DirsScope(root, []string{"docs/ops"}, MatchRels(func(rel string) bool {
		return strings.HasSuffix(rel, "/readyz.md") || strings.HasSuffix(rel, "/alerting-rules.md")
	}))
	files, err := LoadContentFiles(sc, []string{".md"})
	require.NoError(t, err, "load saga fanout docs under docs/ops")
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		out[f.Rel] = f.Bytes
	}
	return out
}

// ─── live tests ───────────────────────────────────────────────────────────────

// TestSagaStatusFanoutCoverageC4 enforces the TerminalEventKind ↔ IsTerminal
// exhaustiveness invariant against the live source.
func TestSagaStatusFanoutCoverageC4(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	isTerminal := map[string]bool{}
	tekCases := map[string]bool{}
	var tekRel string
	var tekLine int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case sfcStatusPkgPath:
				set, _, _ := sfcCollectSwitchStatusCases(p, "IsTerminal", sfcStatusTypeName)
				for k := range set {
					isTerminal[k] = true
				}
			case sfcJournalPkgPath:
				set, rel, line := sfcCollectSwitchStatusCases(p, "TerminalEventKind", "")
				for k := range set {
					tekCases[k] = true
				}
				if rel != "" {
					tekRel, tekLine = rel, line
				}
			}
			return nil
		})

	require.NotEmpty(t, isTerminal, "SAGA-STATUS-FANOUT-COVERAGE-01/C4: Status.IsTerminal() terminal set resolved empty — "+
		"either IsTerminal() was refactored away from a switch (see blind-spot B1) or saga.Status was renamed/moved")
	require.NotEmpty(t, tekCases, "SAGA-STATUS-FANOUT-COVERAGE-01/C4: journal.TerminalEventKind case set resolved empty — "+
		"either TerminalEventKind was refactored away from a switch (see blind-spot B1) or saga.Status was renamed/moved")

	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C4", sfcDiagsTerminalEventKind(isTerminal, tekCases, tekRel, tekLine))
}

// TestSagaStatusFanoutCoverageC5 enforces that the go/types declared const set
// of saga.Status / journal.EventKind is identical to the value set Render()
// enumerates via `for v := <start>; v.Valid(); v++`. This closes the formerly
// dismissed blind-spot B2: a const added without extending Valid() is invisible
// to Render() and the golden lock, but C5 turns that divergence into a red test —
// making the "saga.Status / journal.EventKind const set is the single source of
// truth" claim strictly true rather than "the Valid()-admitted range is".
func TestSagaStatusFanoutCoverageC5(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	statusDeclared := map[int64]string{}
	kindDeclared := map[int64]string{}
	var statusValidRel, kindValidRel string
	var statusValidLine, kindValidLine int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case sfcStatusPkgPath:
				statusDeclared, statusValidRel, statusValidLine = sfcCollectDeclaredConsts(p, sfcStatusPkgPath, sfcStatusTypeName)
			case sfcJournalPkgPath:
				kindDeclared, kindValidRel, kindValidLine = sfcCollectDeclaredConsts(p, sfcJournalPkgPath, sfcKindTypeName)
			}
			return nil
		})

	// Floor guards (not vacuous passes): an empty declared set means the type was
	// renamed/moved or the package failed to load; a zero Valid() line means
	// Valid() was renamed or refactored away from a method.
	require.NotEmpty(t, statusDeclared, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: saga.Status declared const set "+
		"resolved empty — type renamed/moved or package load failed")
	require.NotEmpty(t, kindDeclared, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: journal.EventKind declared const set "+
		"resolved empty — type renamed/moved or package load failed")
	require.NotZero(t, statusValidLine, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: saga.Status.Valid() method not found "+
		"— renamed or refactored away")
	require.NotZero(t, kindValidLine, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: journal.EventKind.Valid() method not found "+
		"— renamed or refactored away")

	var diags []Diagnostic
	diags = append(diags,
		sfcDiagsConstSetValidRange("saga.Status", statusDeclared, sfcStatusLoopValues(t), statusValidRel, statusValidLine)...)
	diags = append(diags,
		sfcDiagsConstSetValidRange("journal.EventKind", kindDeclared, sfcKindLoopValues(t), kindValidRel, kindValidLine)...)
	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C5", diags)
}

// TestSagaCoverageGolden enforces that the three generated fanout artifacts are
// byte-identical to a fresh Render() of the const set.
func TestSagaCoverageGolden(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping content-load archtest in -short mode")
	}
	root := findModuleRoot(t)

	art, err := sagacoveragegen.Render()
	require.NoError(t, err, "render saga coverage artifacts")

	gen := sfcReadFile(t, root, "kernel/saga/sagajournaltest", sfcGenFileRel, ".go")
	docs := sfcLoadDocs(t, root)
	require.NotEmpty(t, docs[sfcReadyzDocRel], "load %s", sfcReadyzDocRel)
	require.NotEmpty(t, docs[sfcAlertingDocRel], "load %s", sfcAlertingDocRel)

	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/GOLDEN",
		sfcGoldenDiags(art, gen, docs[sfcReadyzDocRel], docs[sfcAlertingDocRel]))
}

// ─── RED-fixture + diagnostic-location self-checks ────────────────────────────

// TestSagaStatusFanoutCoverageC4_REDFixture proves the C4 detection path fires on
// either-direction drift between the IsTerminal() set and the TerminalEventKind
// switch case set.
func TestSagaStatusFanoutCoverageC4_REDFixture(t *testing.T) {
	t.Parallel()
	terminal := map[string]bool{"StatusSucceeded": true, "StatusFailed": true}
	assert.Empty(t, sfcDiagsTerminalEventKind(terminal, terminal, sfcJournalPkgPath, 1),
		"matching sets must yield zero C4 diags")

	missingCase := map[string]bool{"StatusSucceeded": true} // StatusFailed absent from TerminalEventKind
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, missingCase, sfcJournalPkgPath, 1),
		"terminal status missing a TerminalEventKind case must fire C4")

	extraCase := map[string]bool{"StatusSucceeded": true, "StatusFailed": true, "StatusRunning": true}
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, extraCase, sfcJournalPkgPath, 1),
		"TerminalEventKind case not classified terminal by IsTerminal() must fire C4")
}

// TestSagaStatusFanoutCoverageC5_REDFixture proves the C5 detection path fires on
// either-direction drift between the declared const set and the Valid() loop, and
// is silent when they agree.
func TestSagaStatusFanoutCoverageC5_REDFixture(t *testing.T) {
	t.Parallel()
	declared := map[int64]string{1: "StatusPending", 2: "StatusRunning"}
	loop := map[int64]bool{1: true, 2: true}
	assert.Empty(t, sfcDiagsConstSetValidRange("saga.Status", declared, loop, "kernel/saga/status.go", 42),
		"aligned const set and Valid() loop must yield zero C5 diags")

	// The user-reported gap: a const is added (value 3) but Valid() is not
	// extended, so the `s.Valid()` loop stops at 2 and never sees value 3.
	declaredExtra := map[int64]string{1: "StatusPending", 2: "StatusRunning", 3: "StatusAborted"}
	assert.NotEmpty(t, sfcDiagsConstSetValidRange("saga.Status", declaredExtra, loop, "kernel/saga/status.go", 42),
		"a declared const outside the Valid() loop range must fire C5")

	// The inverse: Valid() admits a value that no declared const carries.
	loopExtra := map[int64]bool{1: true, 2: true, 3: true}
	assert.NotEmpty(t, sfcDiagsConstSetValidRange("saga.Status", declared, loopExtra, "kernel/saga/status.go", 42),
		"a Valid()-admitted value with no declared const must fire C5")
}

// TestSagaCoverageGolden_REDFixture proves the golden detection path fires when
// any of the three artifacts drifts, and is silent when all align.
func TestSagaCoverageGolden_REDFixture(t *testing.T) {
	t.Parallel()
	art, err := sagacoveragegen.Render()
	require.NoError(t, err)

	readyz := []byte(sagacoveragegen.ReadyzTableStartMarker + "\n" + art.ReadyzTable + sagacoveragegen.ReadyzTableEndMarker + "\n")
	alerting := []byte(sagacoveragegen.KindLegendStartMarker + "\n" + art.KindLegend + sagacoveragegen.KindLegendEndMarker + "\n")

	assert.Empty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, readyz, alerting),
		"aligned artifacts must yield zero golden diags")

	assert.NotEmpty(t, sfcGoldenDiags(art, []byte("// drifted\n"), readyz, alerting),
		"a drifted gen file must fire golden")
	driftedReadyz := []byte(sagacoveragegen.ReadyzTableStartMarker + "\n| drift |\n" + sagacoveragegen.ReadyzTableEndMarker + "\n")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, driftedReadyz, alerting),
		"a drifted readyz region must fire golden")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, []byte("no markers here\n"), alerting),
		"a readyz doc missing its markers must fire golden")
	driftedLegend := []byte(sagacoveragegen.KindLegendStartMarker + "\n drift \n" + sagacoveragegen.KindLegendEndMarker + "\n")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, readyz, driftedLegend),
		"a drifted legend region must fire golden")
}

// TestSagaCoverageDiagnosticLocations asserts every emitted diagnostic carries a
// real module-relative Rel and a non-zero Line — no import-path Rel, no :0:
// (the F3 fix; guards against regressing to un-navigable locations).
func TestSagaCoverageDiagnosticLocations(t *testing.T) {
	t.Parallel()
	art, err := sagacoveragegen.Render()
	require.NoError(t, err)

	var all []Diagnostic
	all = append(all, sfcDiagsTerminalEventKind(
		map[string]bool{"StatusSucceeded": true},
		map[string]bool{"StatusFailed": true},
		"kernel/saga/journal/event.go", 135)...)
	all = append(all, sfcGoldenDiags(art, []byte("drift\n"),
		[]byte("no markers"), []byte("no markers"))...)
	all = append(all, sfcDiagsConstSetValidRange("saga.Status",
		map[int64]string{9: "StatusAborted"}, map[int64]bool{1: true},
		"kernel/saga/status.go", 42)...)

	require.NotEmpty(t, all, "self-check must exercise at least one diagnostic")
	for _, d := range all {
		assert.NotZero(t, d.Line, "diagnostic %q must carry a non-zero Line", d.Message)
		assert.False(t, strings.Contains(d.Rel, "github.com/"),
			"diagnostic Rel %q must be a module-relative path, not an import path", d.Rel)
		assert.NotEmpty(t, d.Rel, "diagnostic must carry a Rel")
	}
}

// ============================================================================
// SAGA-STEP-RUN-OUTSIDE-TX-01   (from saga_step_run_outside_tx_test.go)
// ============================================================================
// INVARIANT: SAGA-STEP-RUN-OUTSIDE-TX-01
//
// SAGA-STEP-RUN-OUTSIDE-TX-01 — call-discipline lock for the Coordinator
// step executor.
//
// Inside all production .go files in runtime/saga/ (excluding _test.go),
// kernel/saga.StepFunc invocations are confined to the safeRun helper, AND
// safeRun MUST NOT be called from inside any TxRunner.RunInTx closure body.
// Together these ensure user step code never runs with a DB transaction held
// open.
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
//	   Every CallExpr in any production .go file under runtime/saga/ (excluding
//	   _test.go) whose callee resolves to kernel/saga.StepFunc type MUST be
//	   inside the body of safeRun.
//	   AI-robust: Hard (typed callsite-uniqueness + posInRanges body gate;
//	   the (callee type, enclosing function) pair is unique — any other shape
//	   fails immediately).
//
//	A2 [TestSagaStepRunOutsideTx_A2_SafeRunNotInsideRunInTxClosure]
//	   For every CallExpr to RunInTx in any production .go file under
//	   runtime/saga/ (excluding _test.go), the closure literal passed as the
//	   second argument (the tx callback body) MUST NOT contain a call to
//	   safeRun. Checked via EachInSubtree over the closure body.
//	   AI-robust: Medium (AST structural check; covers direct safeRun(...) in
//	   the closure body; a helper-wrapper two levels deep is a B2 residual —
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
//	   A2 would miss it. Mitigated by: scope (all runtime/saga/ production .go) +
//	   Coordinator single-authority pattern means no wrapper helpers exist today.
//	   A2 helper-function transitivity (safeRun callsite chase) tracked in
//	   gh issue #980.
//
// ref: tools/archtest/span_setattr_redact_test.go (callsite-uniqueness pattern)
// ref: tools/archtest/aftercommit_pure_transient_test.go (parent-node negative)
// ref: .claude/rules/gocell/ai-robust.md §"Hard 范本目录" "typed marker funnel"

// --- Rule constants ---

const sagaStepRunOutsideTxRule = "SAGA-STEP-RUN-OUTSIDE-TX-01"

// safeRunFuncName is the single sanctioned StepFunc invoker inside coordinator.go.
const safeRunFuncName = "safeRun"

// runInTxMethodName is the method on TxRunner whose closure body must not
// contain a safeRun call.
const runInTxMethodName = "RunInTx"

// sagaRuntimePkgPrefix is the prefix for all production runtime/saga/ files
// enforced by A1, A2, and B1. Both test-file exclusion (_test.go suffix) and
// this prefix guard are applied together.
const sagaRuntimePkgPrefix = "runtime/saga/"

// sagaStepRunFixturesDir is the testdata directory for SAGA-STEP-RUN-OUTSIDE-TX-01
// red/green fixtures.
const sagaStepRunFixturesDir = "saga_step_run_outside_tx_fixtures"

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
// CallExpr in any production .go file under runtime/saga/ (excluding _test.go)
// whose callee resolves to type kernel/saga.StepFunc is inside the body of
// safeRun.
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
//     is not caught — but runtime/saga/ does not define such interfaces.
func TestSagaStepRunOutsideTx_A1_StepFuncCallsiteUniqueness(t *testing.T) {
	t.Parallel()
	diags := RunTyped(t, TypedOpts{}, []string{"./runtime/saga/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		// Enforce on all production .go files in runtime/saga/ (not _test.go).
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isRuntimeSagaProductionFile(rel) {
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
// RunInTx call in any production .go file under runtime/saga/ (excluding
// _test.go) has a safeRun call directly inside its closure argument body.
//
// Detection: walk all CallExprs whose callee selector ends in "RunInTx". For
// each, inspect Args[1] (the closure literal). EachInSubtree inside the
// closure body for any CallExpr whose callee is the Ident "safeRun".
//
// Pure AST: no types needed — "RunInTx" selector match is syntactic.
//
// Residual blind spot B2: a wrapper helper defined outside the RunInTx
// closure that itself calls safeRun — A2's sub-tree scan does not chase
// helper bodies. Documented in package godoc; mitigated by Coordinator
// single-authority pattern (no wrapper helpers exist today).
func TestSagaStepRunOutsideTx_A2_SafeRunNotInsideRunInTxClosure(t *testing.T) {
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
// AI-robust rating: see file-level CommentGroup ("Rated Soft; Hard upgrade
// via typed StepFunc alias detection — gh issue #979"). This blind-spot
// reverse self-test is an existing Soft carve-out for A1's Hard primary
// path; new Soft enforcement is rejected (see .claude/rules/gocell/ai-robust.md).
// Upgrading B1 to Medium requires switching from pure-AST `Run` to typed
// `RunTyped` + resolving the kernel/saga package via TypesInfo.PkgNameOf
// (no longer string-anchored on import path literal) — tracked in #979.
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

// isRuntimeSagaProductionFile reports whether rel is a production .go file
// under runtime/saga/ (i.e., has the sagaRuntimePkgPrefix prefix and does not
// end with _test.go). Used by A1 and A2 to scope the rule to the full
// runtime/saga/ package, not just coordinator.go.
func isRuntimeSagaProductionFile(rel string) bool {
	return strings.HasPrefix(rel, sagaRuntimePkgPrefix) && !strings.HasSuffix(rel, "_test.go")
}

// sagaStepRunFixturePattern returns the (relDir, pattern) pair for the given
// fixture case under sagaStepRunFixturesDir. Mirrors the helper pattern used
// by the saga compensate pure test.
func sagaStepRunFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaStepRunFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaStepRunFixturesDir + "/" + fix
}

// TestSagaStepRunOutsideTx_Detector_RedExtraFileFixture proves that A1 fires
// on a non-coordinator.go file in the fixture tree. The fixture violator.go
// declares a StepFunc-typed variable and calls it directly outside of any
// safeRun body. This confirms the scope extension (A1 now covers all
// runtime/saga/ production files, not just coordinator.go).
//
// The fixture intentionally does NOT have the path runtime/saga/...; A1 in
// fixture mode skips the production scope filter and calls
// checkA1StepFuncCallsites directly on all loaded files.
func TestSagaStepRunOutsideTx_Detector_RedExtraFileFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaStepRunFixturePattern("red_extra_file")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		// No scope filter for fixtures: scan all loaded files directly.
		var ds []Diagnostic
		for _, file := range p.Files {
			ds = append(ds, checkA1StepFuncCallsites(p, file)...)
		}
		return ds
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaStepRunOutsideTx_Detector_RedSafeRunInRunInTxFixture proves that A2
// fires when safeRun() is called directly inside a RunInTx closure body.
// The fixture declares a fake TxRunner-shaped struct (RunInTx method) + a
// local safeRun identifier, then calls safeRun inside the RunInTx closure.
// A2 is a structural (pure-AST) check on method/identifier names, so the
// fixture does not need to import persistence.TxRunner — the syntactic
// match is by method name only.
func TestSagaStepRunOutsideTx_Detector_RedSafeRunInRunInTxFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaStepRunFixturePattern("red_safe_run_in_runintx")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		// No scope filter for fixtures: scan all loaded files directly.
		var ds []Diagnostic
		for _, file := range p.Files {
			ds = append(ds, checkA2SafeRunNotInRunInTx(p, file)...)
		}
		return ds
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// posInRanges (package-level, defined in span_record_error_redact_test.go) and
// collectFuncBodyRanges (defined in span_setattr_redact_test.go) are reused
// directly — both are visible within the archtest package.

// ============================================================================
// SAGA-CONSTRUCTOR-NIL-GUARD-01   (type-aware, Medium)
// ============================================================================

// INVARIANT: SAGA-CONSTRUCTOR-NIL-GUARD-01
//
// # Rule intent
//
// Every top-level New* constructor within the runtime/saga and
// runtime/saga/executor packages that receives a non-variadic parameter whose
// underlying type is an interface must protect that parameter with either
// pkg/validation.IsNilInterface (for interface-typed required deps) or
// kernel/clock.MustHaveClock (for clock.Clock params). The guard must be
// resolved via go/types object identity against the actual parameter object —
// not by string name matching.
//
// # Scope
//
// Packages under scan:
//   - github.com/ghbvf/gocell/runtime/saga
//   - github.com/ghbvf/gocell/runtime/saga/executor
//
// # Guard set (resolved by types.Func pkg path + name)
//
//   - pkg: github.com/ghbvf/gocell/pkg/validation  name: IsNilInterface
//   - pkg: github.com/ghbvf/gocell/kernel/clock     name: MustHaveClock
//
// # AI-robust grading: Medium
//
// Hard is unachievable here: a missing guard is expressible in valid Go (the
// compiler accepts a constructor that omits the nil check). The rule requires
// type-aware static analysis — go/types resolution of parameter identity and
// called function identity — to detect the absence. Medium is the correct
// honest ceiling for "absence of call" rules: the detector is type-aware
// (uses types.Info.Defs for parameter objects and types.Info.ObjectOf for
// callee resolution), so it cannot be fooled by renaming parameters, and
// misidentifying an unrelated IsNilInterface call on a different variable.
//
// # Hard upper-bound path
//
// Migrate runtime/saga constructors to the REQUIRED-DEP-NIL-GUARD-01 funnel
// (gocell:"required" struct tag + generated validateRequired()). That funnel
// carries Hard codegen enforcement. Backlog tracking: gh #1317.
//
// # Anonymous / blank interface parameters (closed, not a blind spot)
//
// An unnamed interface parameter (`func New(journal.Journal)`) or a
// blank-identifier one (`func New(_ journal.Journal)`) has no identifier to
// reference, so it can never carry a guard call. Rather than silently skipping
// it (the original bypass), the detector flags such a param directly: a
// required interface dep MUST be a named, guardable parameter. The
// red_anonymous_param fixture is the reverse self-test for both forms.
//
// # Blind spots
//
//   - Anonymous / blank interface params: CLOSED in the detector (see the
//     "Anonymous / blank" section above); reverse self-test = the
//     red_anonymous_param fixture.
//   - Parenthesized guard arg (IsNilInterface((dep))): CLOSED via ast.Unparen;
//     reverse self-test = NewParenGuarded in the red_unguarded_param fixture
//     (it stays silent, so omitting Unparen would add a false-positive diag and
//     fail the golden).
//   - B1 (helper-indirected required-dep guard): a guard moved out of the
//     constructor body into a helper makes the required param appear unguarded,
//     so it is caught by the main production test
//     (TestSagaConstructorNilGuard_NoUnguardedInterfaceParam), which would turn
//     red. NOT a separate reverse self-test: option setters legitimately guard
//     *optional* deps with validation.IsNilInterface outside constructors
//     (WithTracer / WithObserver / …), so "no guard calls outside New*" is not a
//     valid invariant.
//   - B2 (param reassigned to an alias before guarding, x := j;
//     IsNilInterface(x)): the detector binds the guard's first arg to the
//     *original* parameter object, so an alias would also surface as a false
//     positive in the main test. Reverse self-test
//     TestSagaConstructorNilGuard_BlindSpot_B2_NoParamReassignment additionally
//     asserts the form is absent in production.

// sagaConstructorNilGuardFixturePattern returns the (relDir, pattern) pair for
// a constructor nil-guard fixture sub-directory.
func sagaConstructorNilGuardFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaConstructorFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaConstructorFixturesDir + "/" + fix
}

// sagaConstructorScopePackages is the set of production package paths scanned
// by SAGA-CONSTRUCTOR-NIL-GUARD-01.
var sagaConstructorScopePackages = map[string]bool{
	"github.com/ghbvf/gocell/runtime/saga":          true,
	"github.com/ghbvf/gocell/runtime/saga/executor": true,
}

// sagaGuardFuncKeys is the set of (pkgPath, funcName) pairs that constitute an
// accepted nil-guard call for a constructor parameter.
type sagaGuardKey struct{ pkg, name string }

var sagaGuardFuncKeys = []sagaGuardKey{
	{"github.com/ghbvf/gocell/pkg/validation", "IsNilInterface"},
	{"github.com/ghbvf/gocell/kernel/clock", "MustHaveClock"},
}

// scanConstructorNilGuards scans p for top-level New* functions that accept
// non-variadic interface parameters lacking a guard call. It is a pure function
// (no *testing.T dependency) so it can be shared between the production scan
// and the fixture-based reverse test.
//
// Detection algorithm:
//  1. Walk p.Files for top-level FuncDecl (no Recv) whose name starts with "New"
//     and has a non-nil Body.
//  2. For each non-variadic parameter: resolve its type via
//     p.TypesInfo.TypeOf(fieldType). If the Underlying() is an interface, the
//     parameter is a required interface dep that needs a guard.
//  3. For each such parameter, collect its types.Object via
//     p.TypesInfo.Defs[paramIdent].
//  4. Walk the Body for CallExpr whose callee resolves (via
//     p.TypesInfo.ObjectOf) to a types.Func in sagaGuardFuncKeys, and whose
//     first argument's ObjectOf equals the parameter object.
//  5. If no matching guard call is found, emit a diagnostic.
func scanConstructorNilGuards(p *Pass) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var out []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Recv != nil || fd.Body == nil {
				return
			}
			if !strings.HasPrefix(fd.Name.Name, "New") {
				return
			}
			if fd.Type.Params == nil {
				return
			}
			// Collect interface parameters and their types.Object.
			type ifaceParam struct {
				obj      types.Object
				typeExpr ast.Expr
				name     string
				typStr   string
			}
			var ifaces []ifaceParam
			for _, field := range fd.Type.Params.List {
				if _, isEllipsis := field.Type.(*ast.Ellipsis); isEllipsis {
					// variadic — skip
					continue
				}
				typ := p.TypesInfo.TypeOf(field.Type)
				if typ == nil {
					continue
				}
				if _, isIface := typ.Underlying().(*types.Interface); !isIface {
					continue
				}
				// An unnamed interface parameter (e.g. `func New(journal.Journal)`)
				// has no identifier to reference, so it can never be nil-guarded —
				// flag it directly rather than silently skipping (closes the
				// anonymous-param bypass; the param loop below requires a name).
				if len(field.Names) == 0 {
					out = append(out, sagaDiag(p, field.Type, rel,
						sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
							" has an unnamed interface parameter (type "+sagaShortType(typ)+
							") that cannot be nil-guarded; give it a name and add "+
							"validation.IsNilInterface(<param>) (or clock.MustHaveClock)"))
					continue
				}
				for _, name := range field.Names {
					obj := p.TypesInfo.Defs[name]
					// A blank-identifier interface param (`_ journal.Journal`) has
					// no object to bind a guard to — also a bypass; flag it.
					if name.Name == "_" || obj == nil {
						out = append(out, sagaDiag(p, name, rel,
							sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
								" has a blank-identifier interface parameter (type "+sagaShortType(typ)+
								") that cannot be nil-guarded; give it a name and add "+
								"validation.IsNilInterface(<param>) (or clock.MustHaveClock)"))
						continue
					}
					ifaces = append(ifaces, ifaceParam{
						obj:      obj,
						typeExpr: field.Type,
						name:     name.Name,
						typStr:   sagaShortType(typ),
					})
				}
			}
			if len(ifaces) == 0 {
				return
			}
			// For each interface param, check if a guard call referencing it exists
			// in the body.
			for _, ip := range ifaces {
				_, guarded := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
					if !sagaIsGuardCall(p, call) {
						return false
					}
					// Check first argument is the parameter object. ast.Unparen
					// strips redundant parens so IsNilInterface((dep)) is still
					// recognized as a guard (closes the parenthesized-arg bypass).
					if len(call.Args) == 0 {
						return false
					}
					firstArg, ok2 := ast.Unparen(call.Args[0]).(*ast.Ident)
					if !ok2 {
						return false
					}
					return p.TypesInfo.ObjectOf(firstArg) == ip.obj
				})
				if !guarded {
					out = append(out, sagaDiag(p, fd.Name, rel,
						sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
							" has interface parameter "+ip.name+" (type "+ip.typStr+
							") without a guard call (IsNilInterface or clock.MustHaveClock); "+
							"add validation.IsNilInterface("+ip.name+") or clock.MustHaveClock("+ip.name+", ...)"))
				}
			}
		})
	}
	return out
}

// sagaIsGuardCall reports whether call resolves (via go/types) to a sanctioned
// nil-guard function — validation.IsNilInterface or clock.MustHaveClock. Shared
// by scanConstructorNilGuards and the B1 reverse self-test so both key on the
// same callee identity (not a string name match).
func sagaIsGuardCall(p *Pass, call *ast.CallExpr) bool {
	var calleeFunc *types.Func
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if f, ok := p.TypesInfo.ObjectOf(fun.Sel).(*types.Func); ok {
			calleeFunc = f
		}
	case *ast.Ident:
		if f, ok := p.TypesInfo.ObjectOf(fun).(*types.Func); ok {
			calleeFunc = f
		}
	}
	if calleeFunc == nil || calleeFunc.Pkg() == nil {
		return false
	}
	for _, gk := range sagaGuardFuncKeys {
		if calleeFunc.Pkg().Path() == gk.pkg && calleeFunc.Name() == gk.name {
			return true
		}
	}
	return false
}

// sagaShortType renders t as package.TypeName (short package name, no full
// import path) to keep diagnostics readable in CI logs.
func sagaShortType(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

// sagaConstructorIfaceParamObjs returns the set of named, non-variadic interface
// parameter objects of a New* constructor — the params scanConstructorNilGuards
// requires a guard for. Used by the B2 reverse self-test.
func sagaConstructorIfaceParamObjs(p *Pass, fd *ast.FuncDecl) map[types.Object]bool {
	out := map[types.Object]bool{}
	if fd.Type.Params == nil {
		return out
	}
	for _, field := range fd.Type.Params.List {
		if _, isEllipsis := field.Type.(*ast.Ellipsis); isEllipsis {
			continue
		}
		typ := p.TypesInfo.TypeOf(field.Type)
		if typ == nil {
			continue
		}
		if _, isIface := typ.Underlying().(*types.Interface); !isIface {
			continue
		}
		for _, name := range field.Names {
			if obj := p.TypesInfo.Defs[name]; obj != nil {
				out[obj] = true
			}
		}
	}
	return out
}

// TestSagaConstructorNilGuard_NoUnguardedInterfaceParam asserts that every
// top-level New* constructor in the runtime/saga and runtime/saga/executor
// packages guards every non-variadic interface parameter with either
// validation.IsNilInterface or clock.MustHaveClock. Production currently has
// 2 constructors: NewCoordinator (runtime/saga) and NewExecutor
// (runtime/saga/executor), both already compliant.
//
// AI-robust grading: Medium — see INVARIANT godoc above.
//
// Blind spots:
//   - B1: guard inside a called helper (not direct in constructor body)
//   - B2: guard called on a reassigned alias of the parameter variable
func TestSagaConstructorNilGuard_NoUnguardedInterfaceParam(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || !sagaConstructorScopePackages[p.Pkg.Path()] {
			return nil
		}
		return scanConstructorNilGuards(p)
	})
	Report(t, sagaConstructorNilGuardRuleID, diags)
}

// TestSagaConstructorNilGuard_Detector_RedUnguardedParamFixture loads the
// red_unguarded_param fixture and asserts the detector fires for the unguarded
// interface parameter. This is the reverse self-test: if the detection logic is
// broken, this test fails even though the production scan yields 0 diagnostics.
//
// Fixture layout:
//   - unguarded constructor: NewUnguarded(dep MyInterface) — no guard → RED
//   - guarded constructor: NewGuarded(dep MyInterface) with IsNilInterface — GREEN
func TestSagaConstructorNilGuard_Detector_RedUnguardedParamFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaConstructorNilGuardFixturePattern("red_unguarded_param")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, scanConstructorNilGuards)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaConstructorNilGuard_Detector_RedAnonymousParamFixture loads the
// red_anonymous_param fixture and asserts the detector fires for interface
// parameters that have no usable identifier — an unnamed param (NewUnnamed) and
// a blank-identifier param (NewBlank). This closes the anonymous-param bypass:
// without naming, a required interface dep cannot carry a guard call. NewNamed
// (named + guarded) is the negative control and must stay silent.
func TestSagaConstructorNilGuard_Detector_RedAnonymousParamFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaConstructorNilGuardFixturePattern("red_anonymous_param")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, scanConstructorNilGuards)
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaConstructorNilGuard_BlindSpot_B2_NoParamReassignment closes blind spot
// B2 (param alias). The detector binds the guard's first argument to the
// *original* parameter object identity; a constructor that copies the param to a
// new variable before guarding (x := dep; IsNilInterface(x)) would defeat that
// binding. This reverse self-test asserts no production New* constructor body
// assigns an interface parameter to another variable.
func TestSagaConstructorNilGuard_BlindSpot_B2_NoParamReassignment(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || !sagaConstructorScopePackages[p.Pkg.Path()] || p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
				continue
			}
			rel := p.Rel(file)
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body == nil || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "New") {
					return
				}
				paramObjs := sagaConstructorIfaceParamObjs(p, fd)
				if len(paramObjs) == 0 {
					return
				}
				EachInSubtree[ast.AssignStmt](fd.Body, func(as *ast.AssignStmt) {
					for _, rhs := range as.Rhs {
						id, ok := ast.Unparen(rhs).(*ast.Ident)
						if !ok {
							continue
						}
						if paramObjs[p.TypesInfo.ObjectOf(id)] {
							out = append(out, sagaDiag(p, as, rel,
								sagaConstructorNilGuardRuleID+"/B2: constructor "+fd.Name.Name+
									" assigns interface parameter "+id.Name+" to another variable; the guard "+
									"detector binds by the original parameter identity — guard "+id.Name+" directly"))
						}
					}
				})
			})
		}
		return out
	})
	Report(t, sagaConstructorNilGuardRuleID+"/B2", diags)
}

// ============================================================================
// SAGA-INVARIANTS-FILE-CONSOLIDATED-01   (new — consolidation guard, Refs #1213)
// ============================================================================

// INVARIANT: SAGA-INVARIANTS-FILE-CONSOLIDATED-01
//
// Every saga-theme archtest invariant MUST live in this single consolidated
// file. Concretely: any tools/archtest/*_test.go that declares a
// `// INVARIANT: SAGA-…` line must be saga_invariants_test.go itself. A future
// re-split (a new saga_<rule>_test.go carrying a SAGA-* invariant) turns this
// test red — closing the recurrence vector that let the saga theme fragment
// into 8 separate files before #1213 consolidated them.
//
// # Why this guard exists (root cause, not just symptom)
//
// .claude/rules/gocell/ai-robust.md §"archtest 文件命名" mandates "同主题规则 ≥ 3
// → {theme}_invariants_test.go". That convention was Soft (no machine guard),
// so the saga theme silently accumulated 8 files. Manually merging them (the
// #1213 cleanup) WITHOUT upgrading the enforcement would leave the Soft hole
// open — the charter forbids patching at the Soft layer. This rule IS the
// upgrade: a Soft naming convention promoted to a machine-enforced Medium, for
// the saga theme.
//
// # AI-robust grading: Medium (content scan + cross-file invariant)
//
// True Hard is infeasible: "which file a rule lives in" is a meta-property over
// the file SET; no codegen funnel or type-system seal can make a split
// unexpressible (Go compiles another file just fine). Medium is the honest
// ceiling. The repo-wide generalization of this guard to ALL themes
// (cell / projection / contract / …) is tracked in #1279.
//
//   - Downstream: a SAGA-* invariant declared outside this file fails the live
//     scan (sagaConsolidationDiags emits a Diagnostic naming the stray file+ID).
//   - Upstream (non-vacuous): the floor guard requires this file to itself carry
//     ≥3 SAGA-* IDs, so the rule cannot pass vacuously if the file is renamed,
//     emptied, or the loader returns nothing.
//
// # Blind-spot catalog + reverse self-checks
//
//   - B1 (non-SAGA-prefixed saga rule): a saga rule whose INVARIANT ID does not
//     start with "SAGA-" escapes the theme key. TestSagaInvariantsConsolidated_
//     BlindSpot_KnownIDsPresent asserts every known saga ID is present here and
//     SAGA-prefixed — catching both a dropped ID and a convention drift.
//   - B2 (comment location): the scan reads every parsed CommentGroup, not only
//     the header one, so a stray mid-file `// INVARIANT: SAGA-…` annotation is
//     still caught. Conversely, because detection runs over ast.File.Comments
//     (parser.ParseComments) and not raw lines, a SAGA token in a string literal
//     or other code text is structurally excluded — it cannot masquerade as a
//     declaration.
//   - RED fixture: TestSagaInvariantsConsolidated_REDFixture drives the real
//     entry point — scanSagaInvariantDecls (the comment parser) →
//     sagaConsolidationDiags — on synthetic *source files*: a SAGA-* anchor in
//     saga_stray_test.go → non-empty; all anchors in saga_invariants_test.go →
//     empty; and a string-literal SAGA token in the home file is not counted.
//     Each diagnostic is asserted to carry a real Rel + non-zero Line + the
//     stray ID.
//
// ref: tools/archtest/archtest_verify_coverage_test.go (directory content-scan pattern)
// ref: .claude/rules/gocell/ai-robust.md §"archtest 文件命名" (the convention this enforces)

const sagaInvariantsConsolidatedRule = "SAGA-INVARIANTS-FILE-CONSOLIDATED-01"

// sagaConsolidatedFile is the single sanctioned home for saga-theme invariants.
const sagaConsolidatedFile = "saga_invariants_test.go"

// sagaConsolidatedFileMinIDs is the floor for the non-vacuous guard: the
// consolidated file must declare at least this many SAGA-* IDs, else a
// rename/empty/scan-regression would let TestSagaInvariantsConsolidated pass
// vacuously. 3 is the structural floor (well below the actual 9 declared today).
const sagaConsolidatedFileMinIDs = 3

// sagaThemePrefix is the theme key the consolidation guard scans for: a saga
// INVARIANT is any anchor whose parsed ID carries this prefix (the SAGA- family
// this file consolidates — see knownSagaInvariantIDs).
const sagaThemePrefix = "SAGA-"

// sagaThemeHit is one saga INVARIANT declaration: its ID and 1-based line.
type sagaThemeHit struct {
	id   string
	line int
}

// scanSagaInvariantDecls returns, keyed by file basename, the saga INVARIANT IDs
// each content file declares. Detection runs over the *parsed comment groups*
// (parser.ParseComments → ast.File.Comments → parseInventoryAnchor) — the same
// canonical anchor path INVENTORY-ANCHOR-VALID-ID-01 uses — NOT a raw line/regex
// scan. So an "INVARIANT: SAGA-…" token sitting in a string literal or any other
// non-comment code text is structurally excluded by the Go parser; only genuine
// `// INVARIANT:` / `// - INVARIANT:` comment declarations count. A "saga
// INVARIANT" is one whose parsed ID carries sagaThemePrefix.
//
// Pure (no *testing.T) so the RED fixture and the live test share one detection
// core. A parse failure surfaces as an error rather than a silent drop
// (fail-closed): a file that does not compile cannot quietly evade the scan.
func scanSagaInvariantDecls(files []ContentContext) (map[string][]sagaThemeHit, error) {
	out := map[string][]sagaThemeHit{}
	for _, f := range files {
		base := path.Base(f.Rel)
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f.Rel, f.Bytes, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: parse %s for saga invariant scan: %w", sagaInvariantsConsolidatedRule, f.Rel, err)
		}
		for _, group := range af.Comments {
			for _, c := range group.List {
				ref, ok := parseInventoryAnchor(c.Text)
				if !ok || !strings.HasPrefix(ref.id, sagaThemePrefix) {
					continue
				}
				out[base] = append(out[base], sagaThemeHit{
					id:   ref.id,
					line: fset.Position(c.Pos()).Line,
				})
			}
		}
	}
	return out, nil
}

// relOfBases maps each content file's basename to its module-relative path — the
// lookup sagaConsolidationDiags needs to locate a stray declaration. Shared by
// the live test and the RED fixture so both feed the diag core identically.
func relOfBases(files []ContentContext) map[string]string {
	relOf := make(map[string]string, len(files))
	for _, f := range files {
		relOf[path.Base(f.Rel)] = f.Rel
	}
	return relOf
}

// sagaConsolidationDiags is the pure detection core: any saga INVARIANT ID
// declared in a file other than sagaConsolidatedFile is a violation. relOf maps
// a basename to its module-relative path for the Diagnostic location; callers
// build it from the same file set as byFile, so every offending base resolves
// (a missing entry would surface as an empty Rel, never a silent drop).
func sagaConsolidationDiags(byFile map[string][]sagaThemeHit, relOf map[string]string) []Diagnostic {
	var diags []Diagnostic
	bases := make([]string, 0, len(byFile))
	for b := range byFile {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	for _, base := range bases {
		if base == sagaConsolidatedFile {
			continue
		}
		for _, hit := range byFile[base] {
			diags = append(diags, Diagnostic{
				Rel:  relOf[base],
				Line: hit.line,
				Message: fmt.Sprintf(
					"saga-theme invariant %s declared in %s — saga invariants must be consolidated into %s "+
						"(per .claude/rules/gocell/ai-robust.md §\"archtest 文件命名\"; move it to %s)",
					hit.id, base, sagaConsolidatedFile, sagaConsolidatedFile),
			})
		}
	}
	return diags
}

// loadArchtestTestFiles loads only the top-level (directly under tools/archtest/)
// *_test.go files as raw content, via the sanctioned scope reader (no os.ReadFile).
// The underlying DirsScope walk is recursive; the MatchRels Dir predicate filters
// the result to the top-level files, excluding internal/ and testdata/ subtrees.
func loadArchtestTestFiles(t *testing.T, root string) []ContentContext {
	t.Helper()
	sc := DirsScope(root, []string{"tools/archtest"}, IncludeTests(), MatchRels(func(rel string) bool {
		return filepath.ToSlash(filepath.Dir(rel)) == "tools/archtest" && strings.HasSuffix(rel, "_test.go")
	}))
	files, err := LoadContentFiles(sc, []string{".go"})
	require.NoError(t, err, "load tools/archtest *_test.go for saga consolidation scan")
	return files
}

// TestSagaInvariantsConsolidated enforces SAGA-INVARIANTS-FILE-CONSOLIDATED-01
// against the live tools/archtest tree.
func TestSagaInvariantsConsolidated(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files := loadArchtestTestFiles(t, root)
	require.NotEmpty(t, files, "%s: no archtest test files loaded — scope/loader regression", sagaInvariantsConsolidatedRule)

	relOf := relOfBases(files)
	byFile, err := scanSagaInvariantDecls(files)
	require.NoError(t, err, "%s: scan saga invariant declarations", sagaInvariantsConsolidatedRule)

	// Floor guard (non-vacuous): the consolidated file must itself carry the saga
	// theme, or a rename/empty would let the rule pass vacuously.
	require.GreaterOrEqual(t, len(byFile[sagaConsolidatedFile]), sagaConsolidatedFileMinIDs,
		"%s: %s declares <%d saga INVARIANT IDs — consolidation file missing/renamed or scan broken",
		sagaInvariantsConsolidatedRule, sagaConsolidatedFile, sagaConsolidatedFileMinIDs)

	Report(t, sagaInvariantsConsolidatedRule, sagaConsolidationDiags(byFile, relOf))
}

// knownSagaInvariantIDs is the set of saga-theme invariants consolidated by
// #1213 (the 8 merged rules + this guard). It is the reverse self-check oracle
// for blind-spot B1.
var knownSagaInvariantIDs = []string{
	"SAGA-STEP-COMPENSATE-PURE-01",
	"SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01",
	"SAGA-EXECUTOR-RAND-INJECTED-01",
	"SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01",
	"SAGA-JOURNAL-HOLDER-SEAL-01",
	"SAGA-DRIVE-BEHIND-LEADER-GATE-01",
	"SAGA-STATUS-FANOUT-COVERAGE-01",
	"SAGA-STEP-RUN-OUTSIDE-TX-01",
	"SAGA-INVARIANTS-FILE-CONSOLIDATED-01",
	"SAGA-CONSTRUCTOR-NIL-GUARD-01",
}

// TestSagaInvariantsConsolidated_BlindSpot_KnownIDsPresent closes blind-spot B1:
// every known saga invariant must be present in the consolidated file and carry
// the SAGA- theme prefix the scan keys on.
func TestSagaInvariantsConsolidated_BlindSpot_KnownIDsPresent(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	sc := DirsScope(root, []string{"tools/archtest"}, IncludeTests(), MatchRels(func(rel string) bool {
		return path.Base(rel) == sagaConsolidatedFile
	}))
	files, err := LoadContentFiles(sc, []string{".go"})
	require.NoError(t, err)
	require.Len(t, files, 1, "expected exactly one %s", sagaConsolidatedFile)

	byFile, err := scanSagaInvariantDecls(files)
	require.NoError(t, err)
	present := map[string]bool{}
	for _, hit := range byFile[sagaConsolidatedFile] {
		present[hit.id] = true
	}
	for _, id := range knownSagaInvariantIDs {
		assert.True(t, strings.HasPrefix(id, "SAGA-"), "known saga ID %q must keep the SAGA- theme prefix", id)
		assert.True(t, present[id], "known saga invariant %q missing from %s — dropped in merge or renamed", id, sagaConsolidatedFile)
	}
}

// TestSagaInvariantsConsolidated_REDFixture proves the detection core fires on a
// stray saga invariant and is silent when all are consolidated. It drives
// synthetic source files through the SAME entry point as the live test —
// scanSagaInvariantDecls (parser.ParseComments) → sagaConsolidationDiags — so it
// exercises the comment parser, not a hand-built parse result. Two properties:
//
//   - A SAGA- token inside a string literal (non-comment code text) must NOT be
//     counted as a declaration: the parser-based scan excludes it structurally.
//   - A genuine `// INVARIANT: SAGA-…` anchor in a file other than the
//     consolidated home fires exactly one diagnostic, pinned to the stray file.
func TestSagaInvariantsConsolidated_REDFixture(t *testing.T) {
	t.Parallel()

	// Consolidated home: three real anchors (header-list + plain forms) plus a
	// string literal that mentions a SAGA token — the latter must be ignored.
	consolidatedSrc := []byte("//   - INVARIANT: SAGA-A-01\n" +
		"// INVARIANT: SAGA-B-01\n" +
		"// INVARIANT: SAGA-C-01\n" +
		"package archtest\n" +
		"\n" +
		"// stringLiteralDecoy must not be parsed as an INVARIANT declaration.\n" +
		"const stringLiteralDecoy = \"INVARIANT: SAGA-NOT-A-DECL-01\"\n")

	consolidatedOnly := []ContentContext{
		{Rel: "tools/archtest/" + sagaConsolidatedFile, Bytes: consolidatedSrc},
	}
	byFile, err := scanSagaInvariantDecls(consolidatedOnly)
	require.NoError(t, err)
	require.Len(t, byFile[sagaConsolidatedFile], 3,
		"only the three comment anchors must be counted; the string-literal SAGA token must be excluded by the parser")
	assert.Empty(t, sagaConsolidationDiags(byFile, relOfBases(consolidatedOnly)),
		"all saga invariants in the consolidated file must yield zero diags")

	// Stray declaration in a sibling file: a genuine comment anchor.
	straySrc := []byte("// INVARIANT: SAGA-STRAY-01\n" +
		"package archtest\n")
	strayed := []ContentContext{
		{Rel: "tools/archtest/" + sagaConsolidatedFile, Bytes: consolidatedSrc},
		{Rel: "tools/archtest/saga_stray_test.go", Bytes: straySrc},
	}
	byFile, err = scanSagaInvariantDecls(strayed)
	require.NoError(t, err)
	diags := sagaConsolidationDiags(byFile, relOfBases(strayed))
	require.NotEmpty(t, diags, "a saga invariant outside the consolidated file must fire the rule")
	// Exactly one diag: the consolidated-file hits must be suppressed, only the
	// stray ID reported — guards against a regression that diags the home file.
	require.Len(t, diags, 1, "only the stray file's invariant should be reported; consolidated-file hits must be suppressed")
	d := diags[0]
	assert.NotZero(t, d.Line, "diagnostic must carry a non-zero Line")
	assert.Equal(t, "tools/archtest/saga_stray_test.go", d.Rel, "diagnostic must point at the stray file")
	assert.Contains(t, d.Message, "SAGA-STRAY-01", "diagnostic must name the stray invariant ID")
}
