// INVARIANT: SAGA-STEP-COMPENSATE-PURE-01
//
// saga_compensate_pure_test.go — funnel guarding saga.CompensateFunc purity.
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
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	sagaCompensatePureRuleID = "SAGA-STEP-COMPENSATE-PURE-01"
	sagaPkgPath              = "github.com/ghbvf/gocell/kernel/saga"
	sagaCompensateFuncName   = "CompensateFunc"
	sagaCompensateFieldName  = "Compensate"
	sagaFixturesDir          = "saga_compensate_pure_fixtures"
	sagaMaxCompensateStmts   = 10
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
