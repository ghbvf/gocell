// INVARIANT: SAGA-STEP-COMPENSATE-PURE-01
//
// saga_compensate_pure_test.go — funnel guarding saga.CompensateFunc purity.
// Compensate is the pure-reverse rollback for one previously-committed step.
// The Coordinator owns the transaction layer; Compensate runs in the
// application domain only and MUST NOT perform durable/persistent side effects.
//
// Detection mechanism:
//
//  1. Identify all production AST positions where a function literal is the
//     assigned value of a kernel/saga.CompensateFunc-typed expression.
//     Detection uses typed AST (RunTypedProduction / RunTypedFixture):
//     scan ValueSpec, AssignStmt, and CompositeLit KeyValue (Step.Compensate
//     field). The match is on the *declared type* of the LHS / field, resolved
//     via TypesInfo.TypeOf / TypesInfo.Defs — NOT a string anchor on the name
//     "Compensate".
//  2. For each such function literal body, walk the AST for forbidden CallExpr
//     targets. The forbidden call set is closed:
//     - outbox.Writer.Write (interface in kernel/outbox)
//     - outbox.Emitter.Emit (interface in kernel/outbox)
//     - persistence.TxRunner.RunInTx (interface in kernel/persistence)
//     - *database/sql.Tx methods (Commit, Rollback, Exec, Query, QueryRow, Stmt, Prepare)
//     - pgx.Tx methods (Commit, Rollback, Exec, Query, QueryRow)
//     Receiver type is resolved via TypesInfo.TypeOf(sel.X); interface
//     implementors are caught via types.Implements (e.g. a CellWriter that
//     wraps outbox.Writer is also flagged).
//
// AI-robust:
//
//   - A1 [TestSagaStepCompensatePure_A1_NoForbiddenCallsInCompensateBody /
//     TestSagaStepCompensatePure_Detector_RedFixture]
//     Typed AST: forbidden-call detection uses TypesInfo.TypeOf(sel.X), NOT a
//     string match on method or type names. A function whose receiver resolves
//     to a banned type is flagged regardless of import alias.
//     AI-robust: Hard primary path (CompensateFunc slot identified by declared
//     type, not name; forbidden call set identified by types.Implements + exact
//     pkg path, not string match) + Medium fallback (when TypesInfo.TypeOf
//     returns nil for a CompositeLit key value, detection falls back to the
//     struct field name string match against sagaCompensateFieldName; this
//     partial-Soft path is accepted under the Hard primary but tracked for
//     removal in gh issue #980).
//
//   - B1 [TestSagaStepCompensatePure_BlindSpot_B1_BodyStatementCount]
//     A CompensateFunc body may delegate to a package-private helper func
//     that performs the forbidden call. A1 does not transitively chase such
//     helpers. Mitigation: pure compensate bodies are short — B1 asserts no
//     production CompensateFunc literal has more than 10 statements, ensuring
//     that inline inspection covers the bulk of the logic. This is Medium
//     (statement-count discipline, not type-level seal).
//
// Known blind spots:
//
//	B1 — helper-function transitivity: if a CompensateFunc body calls a local
//	     helper that itself touches a banned type, A1 misses it. Mitigated by
//	     the B1 body-count discipline and the structural guarantee that
//	     Compensate receives no tx in its context (Coordinator strips tx from
//	     the ctx it hands to Compensate). Transitivity via call-graph
//	     reachability is deferred — tracked in gh issue #980
//	     (SAGA-STEP-COMPENSATE-PURE-01 B1 → Hard via call-graph, PR-08+).
//	     Same call-graph technique also covers the A1 fallback path
//	     (TypesInfo partial-fail → string-anchor) tracked under the same issue.
//
// Reverse self-tests:
//
//	B1 — statement-count: no production CompensateFunc literal exceeds N=10
//	     statements (forces "short pure body" discipline).
//	Fixture-based detector self-test: testdata/saga_compensate_pure_fixtures/
//	  red_outbox_call/ contains a violating CompensateFunc and asserts the
//	  detector fires.
//
// ref: tools/archtest/aftercommit_pure_transient_test.go (sibling purity pattern).
// ref: tools/archtest/aftercommit_pure_transient_test.go::bannedReceiverLabel
//
//	(shared type-resolution helpers are reused directly).
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"
)

const (
	sagaCompensatePureRuleID = "SAGA-STEP-COMPENSATE-PURE-01"
	sagaPkgPath              = "github.com/ghbvf/gocell/kernel/saga"
	sagaCompensateFuncName   = "CompensateFunc"
	sagaCompensateFieldName  = "Compensate"
	sagaFixturesDir          = "saga_compensate_pure_fixtures"
	sagaMaxCompensateStmts   = 10
)

// sagaBannedReceiverKeys matches afterCommitBannedReceivers shape: the exact
// pkg-path + type-name for concrete banned types (one pointer deref for *sql.Tx).
// Interface types (pgx.Tx, outbox.Writer, outbox.Emitter, persistence.TxRunner)
// are resolved dynamically via sagaResolveBannedReceivers.
var sagaBannedReceiverKeys = map[string]string{
	"database/sql.Tx":                               "*sql.Tx",
	"github.com/jackc/pgx/v5.Tx":                    "pgx.Tx",
	"github.com/ghbvf/gocell/kernel/outbox.Writer":  "outbox.Writer",
	"github.com/ghbvf/gocell/kernel/outbox.Emitter": "outbox.Emitter",
}

// sagaBannedIfaceSpecs lists the (pkgPath, typeName, label) tuples for interface
// types that CompensateFunc bodies must not call methods on.
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
// is a banned receiver: the concrete *database/sql.Tx or pgx.Tx (exact name,
// one pointer deref) OR any type that implements a banned interface
// (outbox.Writer / outbox.Emitter / persistence.TxRunner) — types.Implements,
// so sealed wrappers (outbox.WriterEmitter etc.) and other implementors are
// caught, not just the exact named interface.
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

// collectCompensateLiterals walks the AST of file and returns every function
// literal whose declared type is kernel/saga.CompensateFunc. Three assignment
// forms are matched:
//
//  1. var c saga.CompensateFunc = func(...) {...}         (ValueSpec)
//  2. c = func(...) {...} where c is CompensateFunc-typed  (AssignStmt RHS)
//  3. saga.Step{Compensate: func(...) {...}}              (CompositeLit KeyValue)
func collectCompensateLiterals(p *Pass, file *ast.File) []*ast.FuncLit {
	info := p.TypesInfo
	var out []*ast.FuncLit

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			// var c saga.CompensateFunc = func(...){...}
			typ := info.TypeOf(node.Type)
			if typ == nil || !isCompensateFuncType(typ) {
				return true
			}
			for _, val := range node.Values {
				if lit, ok := val.(*ast.FuncLit); ok {
					out = append(out, lit)
				}
			}

		case *ast.AssignStmt:
			// c = func(...){...} — match each LHS/RHS pair
			for i, lhs := range node.Lhs {
				if i >= len(node.Rhs) {
					break
				}
				lhsType := info.TypeOf(lhs)
				if lhsType == nil || !isCompensateFuncType(lhsType) {
					continue
				}
				if lit, ok := node.Rhs[i].(*ast.FuncLit); ok {
					out = append(out, lit)
				}
			}

		case *ast.CompositeLit:
			// saga.Step{Compensate: func(...){...}}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != sagaCompensateFieldName {
					continue
				}
				// Verify the field's declared type is CompensateFunc.
				if lit, ok := kv.Value.(*ast.FuncLit); ok {
					// We check the type of kv.Value — the func literal is
					// implicitly typed as CompensateFunc by the field position.
					// Use TypeOf(kv.Value) or fall back to field lookup.
					valType := info.TypeOf(kv.Value)
					if valType != nil && isCompensateFuncType(valType) {
						out = append(out, lit)
						continue
					}
					// Fallback: check via the struct's field type.
					// (TypeOf may return the underlying func signature rather
					// than the named CompensateFunc alias for func literals.)
					structType := info.TypeOf(node)
					if structType == nil {
						continue
					}
					st := structType
					if ptr, ok := st.(*types.Pointer); ok {
						st = ptr.Elem()
					}
					named, ok := st.(*types.Named)
					if !ok {
						st = structType.Underlying()
					} else {
						st = named.Underlying()
					}
					if s, ok := st.(*types.Struct); ok {
						for fi := 0; fi < s.NumFields(); fi++ {
							f := s.Field(fi)
							if f.Name() == sagaCompensateFieldName && isCompensateFuncType(f.Type()) {
								out = append(out, lit)
								break
							}
						}
					}
				}
			}
		}
		return true
	})
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

// scanCompensatePure implements A1 over one file: collect every CompensateFunc
// literal and check its body for forbidden calls.
func scanCompensatePure(p *Pass, file *ast.File, ifaces []bannedReceiver) []Diagnostic {
	info := p.TypesInfo
	rel := p.Rel(file)
	var out []Diagnostic

	for _, lit := range collectCompensateLiterals(p, file) {
		EachInSubtree[ast.CallExpr](lit.Body, func(call *ast.CallExpr) {
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
	}
	return out
}

// scanCompensateBodyCount implements B1 over one file: every CompensateFunc
// literal must have ≤ sagaMaxCompensateStmts top-level statements.
func scanCompensateBodyCount(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic

	for _, lit := range collectCompensateLiterals(p, file) {
		n := countStmts(lit.Body)
		if n > sagaMaxCompensateStmts {
			out = append(out, sagaDiag(p, lit, rel,
				sagaCompensatePureRuleID+"-B1: CompensateFunc body has too many statements ("+
					itoa(n)+">"+itoa(sagaMaxCompensateStmts)+"); "+
					"pure-reverse compensates must be short — extract logic to a named helper "+
					"and call it from Compensate (B1 blind-spot discipline)"))
		}
	}
	return out
}

// itoa converts int to string without importing strconv (avoid extra import).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func sagaCompensateFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaFixturesDir + "/" + fix
}

// TestSagaStepCompensatePure_A1_NoForbiddenCallsInCompensateBody asserts no
// production CompensateFunc literal calls a banned receiver method. In PR-03
// there are zero CompensateFunc literals in production, so this fires 0
// diagnostics; it guards future Compensate authors.
func TestSagaStepCompensatePure_A1_NoForbiddenCallsInCompensateBody(t *testing.T) {
	diags := RunTypedProduction(t, TypedOpts{Tags: ProductionFlatTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensatePure(p, file, ifaces)...)
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
	diags := RunTypedProduction(t, TypedOpts{Tags: ProductionFlatTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensateBodyCount(p, file)...)
		}
		return out
	})
	Report(t, sagaCompensatePureRuleID+"-B1", diags)
}

// TestSagaStepCompensatePure_Detector_RedFixture loads the
// red_outbox_call fixture and asserts the A1 detector fires with the expected
// diagnostic. This is the detector self-test: if A1's detection logic is
// broken, this test fails even though the production scan yields 0 diagnostics.
func TestSagaStepCompensatePure_Detector_RedFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaCompensateFixturePattern("red_outbox_call")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanCompensatePure(p, file, ifaces)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}
