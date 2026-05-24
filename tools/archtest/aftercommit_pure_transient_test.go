// INVARIANT: AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
//
// aftercommit_pure_transient_test.go — funnel guarding persistence after-commit
// hooks. Hooks run after a durable commit, with the tx stripped from their ctx
// (kernel/persistence.RunAfterCommitHooks), and must perform only transient
// side effects (saga dispatcher kick, cache invalidation, metrics flush, ws
// broadcast) — never a persistent operation.
//
// Funnel entry = persistence.RegisterAfterCommit(ctx, <hook>). Rules:
//
//	A1 [TestAfterCommitHookPureTransient_A1A2_Fixtures / _Production]
//	   The hook argument MUST be a func literal at the call site, so its body is
//	   always inline-inspectable by A2. Named funcs and method values are
//	   rejected.
//	   AI-robust: Hard — (callee=RegisterAfterCommit, arg=FuncLit) form
//	   uniqueness; any other form fails. Constructively closes blind spot B1.
//
//	A2 [TestAfterCommitHookPureTransient_A1A2_Fixtures / _Production]
//	   The hook literal body MUST NOT call a method on a value whose static type
//	   is pgx.Tx, *database/sql.Tx, or outbox.Writer — resolved via
//	   info.TypeOf(sel.X), NOT string anchors. A one-level heuristic also flags a
//	   same-package unexported helper called from the body that itself touches a
//	   banned type (blind spot B4).
//	   AI-robust: Medium (NOT Hard — not over-claimed). Closure purity is
//	   inter-procedurally undecidable: a hook can call a helper >1 level deep or
//	   capture the enclosing txCtx. The floor is raised STRUCTURALLY, not just by
//	   this archtest — RunAfterCommitHooks strips TxCtxKey, so the committed tx is
//	   unreachable through the ctx a hook is handed. There is no low-cost path to
//	   Hard for this rule shape (see B2/B3/B4); this Medium is the honest ceiling.
//
//	A3 [TestAfterCommitHookPureTransient_A3_Fixture / _Production]
//	   persistence.WithAfterCommitRegistry / RunAfterCommitHooks may be called
//	   only from the five TxRunner implementations (+ _test.go).
//	   AI-robust: Hard downstream (callsite identity locked to an allowlist) /
//	   Medium upstream (no type-level seal: the five runners span
//	   cells+examples+adapters+kernel, so there is no shared internal/ boundary to
//	   make the call unrepresentable elsewhere). Upstream Hard-seal tracked in
//	   gh issue #920 (AFTERCOMMIT-DRAIN-CALLER-SEAL).
//
// Blind spots (ai-robust 强制反向自检; each has a reverse self-test below):
//
//	B1 — named-func / method-value hook arg → closed by A1 (fixture red_non_literal).
//	B2 — hook body calls persistence.TxFromContext to fetch the committed tx →
//	     defanged structurally by the TxCtxKey strip; reverse self-test asserts
//	     no production hook body references persistence.TxFromContext.
//	B3 — reflection (reflect.Value.MethodByName) to invoke a tx method → reverse
//	     self-test asserts no production hook body uses MethodByName.
//	B4 — hook body calls a same-package unexported helper that touches the tx →
//	     A2's one-level heuristic catches the simplest case (fixture
//	     red_local_helper); deeper chains remain uncaught (Medium, documented).
//	     Scope caveat: the heuristic resolves only package-level funcs
//	     (packageFuncDecls filters Recv==nil), so a same-package *method* helper
//	     (obj.doWrite() whose body touches the tx) is a B4 deeper-chain miss, not
//	     a separate gap.
//
// ref: spring-tx TransactionSynchronization (afterCommit semantics);
//
//	.claude/rules/gocell/ai-robust.md "typed marker funnel for unbounded ops" +
//	"single sanctioned holder".
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

const (
	afterCommitRuleID      = "AFTERCOMMIT-HOOK-PURE-TRANSIENT-01"
	persistencePkgPath     = "github.com/ghbvf/gocell/kernel/persistence"
	registerAfterCommitFn  = "RegisterAfterCommit"
	txFromContextFn        = "TxFromContext"
	afterCommitFixturesDir = "aftercommit_pure_transient_fixtures"
)

// afterCommitDrainFns are the registry plumbing functions whose callers are
// restricted to TxRunner implementations (rule A3).
var afterCommitDrainFns = map[string]bool{
	"WithAfterCommitRegistry": true,
	"RunAfterCommitHooks":     true,
	"AfterCommitMark":         true,
	"TruncateAfterCommitTo":   true,
}

// afterCommitBannedReceivers maps "<pkgpath>.<TypeName>" → display label for
// types a hook body may not call methods on (rule A2).
var afterCommitBannedReceivers = map[string]string{
	"github.com/jackc/pgx/v5.Tx":                   "pgx.Tx",
	"database/sql.Tx":                              "*sql.Tx",
	"github.com/ghbvf/gocell/kernel/outbox.Writer": "outbox.Writer",
}

// afterCommitDrainCallerAllowlist is the set of production files permitted to
// call the registry plumbing funcs — exactly the five TxRunner implementations
// (rule A3 downstream Hard).
var afterCommitDrainCallerAllowlist = map[string]bool{
	"adapters/postgres/tx_manager.go":                true,
	"kernel/outbox/demo_tx_runner.go":                true,
	"cells/accesscore/internal/mem/store.go":         true,
	"cells/configcore/internal/testutil/testutil.go": true,
	"examples/todoorder/run.go":                      true,
}

// resolvePkgFuncCall resolves a package-level function call (Selector form
// pkg.Fn(...), including a generic instantiation pkg.Fn[T](...)) to its
// declaring package path and name. Returns ok=false for method calls (non-nil
// receiver), local unqualified calls, or non-func selectors.
func resolvePkgFuncCall(info *types.Info, call *ast.CallExpr) (pkgPath, name string, ok bool) {
	sel, isSel := unwrapGenericCallee(call.Fun).(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	fn, isFunc := info.Uses[sel.Sel].(*types.Func)
	if !isFunc || fn.Pkg() == nil {
		return "", "", false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return "", "", false
	}
	return fn.Pkg().Path(), fn.Name(), true
}

// unwrapGenericCallee strips a generic instantiation (Fn[T] / Fn[T1,T2]) to the
// underlying callee expression, so a generic function such as
// persistence.TxFromContext[pgx.Tx] resolves to its SelectorExpr rather than the
// enclosing *ast.IndexExpr / *ast.IndexListExpr (the F4 generic-callee blind spot).
func unwrapGenericCallee(fun ast.Expr) ast.Expr {
	switch f := fun.(type) {
	case *ast.IndexExpr:
		return f.X
	case *ast.IndexListExpr:
		return f.X
	default:
		return fun
	}
}

// bannedReceiver pairs a resolved banned interface with its display label.
type bannedReceiver struct {
	label string
	iface *types.Interface
}

// resolveBannedReceivers resolves the banned *interface* receiver types
// (pgx.Tx, outbox.Writer) from pkg's transitive import closure, once per Pass.
// *database/sql.Tx is a concrete struct and is matched by exact name, not here.
func resolveBannedReceivers(pkg *types.Package) []bannedReceiver {
	var out []bannedReceiver
	for _, b := range []struct{ path, name, label string }{
		{"github.com/jackc/pgx/v5", "Tx", "pgx.Tx"},
		{"github.com/ghbvf/gocell/kernel/outbox", "Writer", "outbox.Writer"},
	} {
		if iface := lookupInterface(pkg, b.path, b.name); iface != nil {
			out = append(out, bannedReceiver{label: b.label, iface: iface})
		}
	}
	return out
}

func lookupInterface(root *types.Package, path, name string) *types.Interface {
	pkg := findImportedPackage(root, path)
	if pkg == nil {
		return nil
	}
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		return nil
	}
	iface, _ := obj.Type().Underlying().(*types.Interface)
	return iface
}

func findImportedPackage(root *types.Package, path string) *types.Package {
	seen := map[string]bool{}
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

// bannedReceiverLabel reports the display label when sel.X's static type is a
// banned receiver: the concrete *database/sql.Tx (exact name, one pointer deref)
// OR any type that implements a banned interface (pgx.Tx / outbox.Writer) —
// types.Implements, so sealed wrappers (outbox.CellWriter) and other
// implementers are caught, not just the exact named interface.
func bannedReceiverLabel(info *types.Info, sel *ast.SelectorExpr, ifaces []bannedReceiver) (string, bool) {
	t := info.TypeOf(sel.X)
	if t == nil {
		return "", false
	}
	// 1. Exact concrete match (deref one pointer level for *sql.Tx).
	nt := t
	if ptr, isPtr := nt.(*types.Pointer); isPtr {
		nt = ptr.Elem()
	}
	if named, isNamed := nt.(*types.Named); isNamed && named.Obj() != nil && named.Obj().Pkg() != nil {
		if label, ok := afterCommitBannedReceivers[named.Obj().Pkg().Path()+"."+named.Obj().Name()]; ok {
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

func implementsBannedIface(t types.Type, iface *types.Interface) bool {
	if types.Implements(t, iface) {
		return true
	}
	if _, isPtr := t.(*types.Pointer); !isPtr {
		return types.Implements(types.NewPointer(t), iface)
	}
	return false
}

// packageFuncDecls maps unexported package-level func name → its FuncDecl across
// all files of the Pass, for the A2 one-level helper heuristic (B4).
func packageFuncDecls(p *Pass) map[string]*ast.FuncDecl {
	decls := map[string]*ast.FuncDecl{}
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Recv == nil && fd.Body != nil {
				decls[fd.Name.Name] = fd
			}
		})
	}
	return decls
}

// helperTouchesBannedReceiver reports whether ident refers to a same-package
// func whose body (one level) calls a banned-receiver method.
func helperTouchesBannedReceiver(p *Pass, ident *ast.Ident, localFuncs map[string]*ast.FuncDecl, ifaces []bannedReceiver) (string, bool) {
	fn, ok := p.TypesInfo.Uses[ident].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != p.Pkg.Path() {
		return "", false
	}
	decl, ok := localFuncs[fn.Name()]
	if !ok {
		return "", false
	}
	var label string
	EachInSubtree[ast.CallExpr](decl.Body, func(c *ast.CallExpr) {
		if sel, isSel := c.Fun.(*ast.SelectorExpr); isSel {
			if l, banned := bannedReceiverLabel(p.TypesInfo, sel, ifaces); banned {
				label = l
			}
		}
	})
	return label, label != ""
}

func afterCommitDiag(p *Pass, node ast.Node, rel, msg string) Diagnostic {
	return Diagnostic{Rel: rel, Line: p.Fset.Position(node.Pos()).Line, Message: msg}
}

// scanRegisterAfterCommitHooks implements A1 + A2 over one file.
func scanRegisterAfterCommitHooks(p *Pass, file *ast.File, localFuncs map[string]*ast.FuncDecl, ifaces []bannedReceiver) []Diagnostic {
	info := p.TypesInfo
	rel := p.Rel(file)
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := resolvePkgFuncCall(info, call)
		if !ok || pkgPath != persistencePkgPath || name != registerAfterCommitFn || len(call.Args) < 2 {
			return
		}
		lit, isLit := call.Args[1].(*ast.FuncLit)
		if !isLit {
			out = append(out, afterCommitDiag(p, call.Args[1], rel,
				afterCommitRuleID+"-A1: RegisterAfterCommit hook must be a func literal at the call site; "+
					"named funcs and method values defeat body inspection"))
			return
		}
		out = append(out, scanHookBodyTransient(p, rel, lit, localFuncs, ifaces)...)
	})
	return out
}

// scanHookBodyTransient implements A2 over one hook literal body.
func scanHookBodyTransient(
	p *Pass, rel string, lit *ast.FuncLit, localFuncs map[string]*ast.FuncDecl, ifaces []bannedReceiver,
) []Diagnostic {
	info := p.TypesInfo
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](lit.Body, func(call *ast.CallExpr) {
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if label, banned := bannedReceiverLabel(info, fun, ifaces); banned {
				out = append(out, afterCommitDiag(p, fun, rel,
					afterCommitRuleID+"-A2: after-commit hook must be transient; it calls a method on "+
						label+" (persistent side effects belong in the tx body)"))
			}
		case *ast.Ident:
			if label, viaHelper := helperTouchesBannedReceiver(p, fun, localFuncs, ifaces); viaHelper {
				out = append(out, afterCommitDiag(p, fun, rel,
					afterCommitRuleID+"-A2: after-commit hook calls local helper "+fun.Name+
						" that touches a "+label+" (one-level heuristic; Medium)"))
			}
		}
	})
	return out
}

// scanAfterCommitDrainCallers implements A3 over one file.
func scanAfterCommitDrainCallers(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	if strings.HasSuffix(rel, "_test.go") || afterCommitDrainCallerAllowlist[filepath.ToSlash(rel)] {
		return nil
	}
	info := p.TypesInfo
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := resolvePkgFuncCall(info, call)
		if !ok || pkgPath != persistencePkgPath || !afterCommitDrainFns[name] {
			return
		}
		out = append(out, afterCommitDiag(p, call, rel,
			afterCommitRuleID+"-A3: persistence."+name+" may only be called from a TxRunner implementation; "+
				rel+" is not in the allowlist"))
	})
	return out
}

func afterCommitFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", afterCommitFixturesDir, fix),
		"./tools/archtest/testdata/" + afterCommitFixturesDir + "/" + fix
}

// TestAfterCommitHookPureTransient_A1A2_Fixtures runs A1 + A2 over the
// intentional-violation fixtures and asserts the diagnostic set via golden.
func TestAfterCommitHookPureTransient_A1A2_Fixtures(t *testing.T) {
	for _, fix := range []string{
		"green",
		"red_non_literal",
		"red_tx_exec",
		"red_outbox_write",
		"red_local_helper",
		"red_cellwriter",
	} {
		fix := fix
		t.Run("fixture_"+fix, func(t *testing.T) {
			root := findModuleRoot(t)
			relDir, pattern := afterCommitFixturePattern(fix)
			diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil {
					return nil
				}
				localFuncs := packageFuncDecls(p)
				ifaces := resolveBannedReceivers(p.Pkg)
				var out []Diagnostic
				for _, file := range p.Files {
					out = append(out, scanRegisterAfterCommitHooks(p, file, localFuncs, ifaces)...)
				}
				return out
			})
			AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
		})
	}
}

// TestAfterCommitHookPureTransient_A1A2_Production asserts no production hook
// violates A1/A2. (No hooks exist yet — saga is a later PR — so the set is
// empty; this guards future hook authors.)
func TestAfterCommitHookPureTransient_A1A2_Production(t *testing.T) {
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		localFuncs := packageFuncDecls(p)
		ifaces := resolveBannedReceivers(p.Pkg)
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRegisterAfterCommitHooks(p, file, localFuncs, ifaces)...)
		}
		return out
	})
	Report(t, afterCommitRuleID+"-A1A2", diags)
}

// TestAfterCommitHookPureTransient_A3_Fixture asserts a non-allowlisted caller
// of the registry plumbing funcs is flagged.
func TestAfterCommitHookPureTransient_A3_Fixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := afterCommitFixturePattern("red_unallowlisted_drain")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanAfterCommitDrainCallers(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestAfterCommitHookPureTransient_A3_Production asserts the registry plumbing
// funcs are called only from the allowlisted TxRunner implementations.
func TestAfterCommitHookPureTransient_A3_Production(t *testing.T) {
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanAfterCommitDrainCallers(p, file)...)
		}
		return out
	})
	Report(t, afterCommitRuleID+"-A3", diags)
}

// TestAfterCommitHookPureTransient_BlindSpots_NoEscapeInProduction is the
// reverse self-test for blind spots B2 (TxFromContext capture) and B3
// (reflection MethodByName) inside production hook bodies. With no production
// hooks yet the set is empty; the check bites when saga adds hooks.
func TestAfterCommitHookPureTransient_BlindSpots_NoEscapeInProduction(t *testing.T) {
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRegisterAfterCommitEscapes(p, file)...)
		}
		return out
	})
	Report(t, afterCommitRuleID+"-BLINDSPOT", diags)
}

// TestAfterCommitHookPureTransient_Escapes_Fixture asserts the B2/B3 escape scan
// (incl. the F4 generic-callee unwrap) flags a hook reaching for the committed
// tx via persistence.TxFromContext[pgx.Tx].
func TestAfterCommitHookPureTransient_Escapes_Fixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := afterCommitFixturePattern("red_txfromcontext")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRegisterAfterCommitEscapes(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// scanRegisterAfterCommitEscapes runs the B2/B3 escape scan over every
// RegisterAfterCommit func-literal hook in file.
func scanRegisterAfterCommitEscapes(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := resolvePkgFuncCall(p.TypesInfo, call)
		if !ok || pkgPath != persistencePkgPath || name != registerAfterCommitFn || len(call.Args) < 2 {
			return
		}
		lit, isLit := call.Args[1].(*ast.FuncLit)
		if !isLit {
			return
		}
		out = append(out, scanHookBodyEscapes(p, rel, lit)...)
	})
	return out
}

// scanHookBodyEscapes flags B2 (persistence.TxFromContext) and B3
// (reflect Value.MethodByName) inside a hook body.
func scanHookBodyEscapes(p *Pass, rel string, lit *ast.FuncLit) []Diagnostic {
	info := p.TypesInfo
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](lit.Body, func(call *ast.CallExpr) {
		if pkgPath, name, ok := resolvePkgFuncCall(info, call); ok &&
			pkgPath == persistencePkgPath && name == txFromContextFn {
			out = append(out, afterCommitDiag(p, call, rel,
				afterCommitRuleID+"-B2: after-commit hook must not call persistence.TxFromContext to reach the committed tx"))
			return
		}
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "MethodByName" {
			out = append(out, afterCommitDiag(p, call, rel,
				afterCommitRuleID+"-B3: after-commit hook must not use reflection (MethodByName) to invoke a tx method"))
		}
	})
	return out
}
