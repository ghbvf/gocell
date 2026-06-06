package archtest

// refresh_invariants.go — importable rule logic for three refresh-theme
// invariants (#1302 M3):
//
//   - REFRESH-CROSS-STORE-TX-01
//   - REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//   - REFRESH-AMBIENT-TX-01
//
// Detection logic lives here (non-test) so it can be compiled by external
// Cell repositories. GoCell's own Test* functions in refresh_invariants_test.go
// call the same Check* — single source, no parallel rule body.
//
// receiverNamedType (used by matchRefreshGuardedMethod) is defined in
// sessionrefresh_no_session_create.go — both files are in the same package
// (archtest), and it must live in a non-test .go to be visible from here.
//
// Platform-symbol paths are anchored to [PlatformModulePath]; the scan SCOPE
// is the running module, supplied by the driver. See external.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// ─── rule ID constants ────────────────────────────────────────────────────────

const (
	ruleRefreshCrossStoreTX01             = "REFRESH-CROSS-STORE-TX-01"
	ruleRefreshInvalidIndexSingleSource01 = "REFRESH-INVALID-INDEX-SINGLE-SOURCE-01"
	ruleRefreshAmbientTX01                = "REFRESH-AMBIENT-TX-01"
)

// ─── platform-symbol path constants (no bare literals) ────────────────────────
//
// refreshStorePkg and sessionStorePkg are declared in
// credential_invalidate_funnel_invariants.go (same package).

const refreshPortsPkg = PlatformModulePath + "/cells/accesscore/internal/ports"

// ─── detection data ────────────────────────────────────────────────────────────

// canonicalInvalidIndexFile is the only file allowed to define DetectInvalidIndexes.
const canonicalInvalidIndexFile = "adapters/postgres/schema_guard.go"

// refreshGuardedMethod identifies a method that, when called from
// sessionrefresh.Service.Refresh, must reside inside the s.txRunner.RunInTx
// closure. Keyed by the method's owning package path, the named receiver
// type, and the method name — three coordinates that survive field renames,
// alias imports, and unrelated shadowed identifiers.
type refreshGuardedMethod struct {
	pkgPath  string
	typeName string
	name     string
}

// refreshGuardedMethods is the closed set of methods that must be called
// inside the RunInTx closure. The set tracks the lookup chain that
// sessionrefresh.Refresh actually executes after PR #482 + PR #490:
//
//	refreshStore.Peek      → presented-token validation
//	sessionStore.Get       → session row fetch (new in PR #482; replaces deleted SessionRepository.GetByID)
//	userRepo.GetByID       → user state + authz_epoch lookup
//	refreshStore.Rotate    → chain rotation (writes refresh_tokens row)
//	refreshStore.RevokeSession (ambient-tx variant) → in-tx cascade revoke; the *Detached
//	                                                  variant is intentionally NOT guarded —
//	                                                  PR #395 cascade paths must commit
//	                                                  independently of the outer transaction.
var refreshGuardedMethods = map[refreshGuardedMethod]struct{}{
	{pkgPath: refreshStorePkg, typeName: "Store", name: "Peek"}:             {},
	{pkgPath: refreshStorePkg, typeName: "Store", name: "Rotate"}:           {},
	{pkgPath: refreshStorePkg, typeName: "Store", name: "RevokeSession"}:    {},
	{pkgPath: sessionStorePkg, typeName: "Store", name: "Get"}:              {},
	{pkgPath: refreshPortsPkg, typeName: "UserRepository", name: "GetByID"}: {},
}

// ─── REFRESH-CROSS-STORE-TX-01 ───────────────────────────────────────────────

// CheckRefreshCrossStoreTX01 runs REFRESH-CROSS-STORE-TX-01 over the running
// module and returns its diagnostics. GoCell's own TestRefreshCrossStoreTX01
// calls it directly — single source.
func CheckRefreshCrossStoreTX01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	patterns := []string{"./cells/accesscore/slices/sessionrefresh/..."}
	diags := Run(t, Typed(TypedOpts{Tests: false}, patterns), scanRefreshCrossStoreTX)
	// Mirror runErrcodeTypedScan / runFunnelDualScan: scan files behind the
	// consumer's build tags too so a (*Service).Refresh hidden by a //go:build
	// directive is not missed. Report's Canonical dedups files seen in both passes.
	if len(cfg.BuildTags) > 0 {
		diags = append(diags, Run(t, Typed(
			TypedOpts{Tests: false, Tags: cfg.BuildTags}, patterns,
		), scanRefreshCrossStoreTX)...)
	}
	return diags
}

// scanRefreshCrossStoreTX walks every production file in pass.Files for a
// (*Service).Refresh method, then applies the four-shape check to each one.
// Reused by TestRefreshCrossStoreTX01_RedFixtureDetected against the fixture
// package; both call sites must use the same scan function so the fixture
// proves the live rule pipeline, not a parallel implementation.
//
// Receiver-type filter (isServiceRefreshMethod) is necessary because the
// sessionrefresh package contains TWO methods named Refresh:
//   - (*Service).Refresh in service.go — the business method with RunInTx
//   - (RefreshAdapter).Refresh in handler.go — the codegen-adapter HTTP shim
//
// Without the filter the HTTP adapter would emit a false-positive
// "exactly 1 s.txRunner.RunInTx" diagnostic because it never wraps a tx.
func scanRefreshCrossStoreTX(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if !isServiceRefreshMethod(fn) {
				return
			}
			out = append(out, analyzeRefreshMethod(p, fn, rel)...)
		})
	}
	return out
}

// isServiceRefreshMethod reports whether fn is `func (*Service) Refresh(...)`
// or `func (Service) Refresh(...)`. Receiver-type filter aligned with the
// sessionrefresh.Service type name; the fixture also names its target
// struct "Service" so the same predicate matches both code paths.
func isServiceRefreshMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || fn.Name == nil || fn.Name.Name != "Refresh" {
		return false
	}
	if len(fn.Recv.List) != 1 {
		return false
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	ident, ok := recv.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "Service"
}

// analyzeRefreshMethod is the per-function predicate behind
// CheckRefreshCrossStoreTX01. It returns one Diagnostic per shape violation
// observed in fn's body. Cognitive complexity is kept ≤ 15 by delegating
// each shape check to a named helper.
func analyzeRefreshMethod(p *Pass, fn *ast.FuncDecl, rel string) []Diagnostic {
	out, closure := analyzeRefreshStructuralShape(p, fn, rel)
	if closure == nil {
		// Structural failure already recorded; cannot proceed with the
		// outside-closure check without a closure to compare against.
		return out
	}
	out = append(out, scanGuardedCallsOutsideClosure(p, fn, closure, rel)...)
	return out
}

// analyzeRefreshStructuralShape covers shape constraints (1)–(3): exactly
// one s.txRunner.RunInTx call, a resolvable closure literal, and ≥ 1 method
// call on `s` inside that closure. Returns the closure for downstream
// guarded-call analysis, or nil when any structural prerequisite failed.
func analyzeRefreshStructuralShape(p *Pass, fn *ast.FuncDecl, rel string) ([]Diagnostic, *ast.FuncLit) {
	line := func(pos token.Pos) int { return p.Fset.Position(pos).Line }

	var runInTxCalls []*ast.CallExpr
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if isTxRunnerRunInTxCall(call) {
			runInTxCalls = append(runInTxCalls, call)
		}
	})
	if len(runInTxCalls) != 1 {
		return []Diagnostic{{
			Rel:  rel,
			Line: line(fn.Pos()),
			Message: fmt.Sprintf("Refresh must contain exactly 1 s.txRunner.RunInTx call "+
				"(found %d) — wrap validate→update→rotate in a single outer transaction",
				len(runInTxCalls)),
		}}, nil
	}

	runInTx := runInTxCalls[0]
	if len(runInTx.Args) < 2 {
		return []Diagnostic{{
			Rel:     rel,
			Line:    line(runInTx.Pos()),
			Message: "Refresh's RunInTx call must have a second argument (the transaction closure)",
		}}, nil
	}

	closure := resolveClosureArg(fn.Body, runInTx.Args[1])
	if closure == nil {
		return []Diagnostic{{
			Rel:  rel,
			Line: line(runInTx.Pos()),
			Message: "Refresh's RunInTx must receive a closure literal — inline func() error or " +
				"a local variable bound to one",
		}}, nil
	}

	if !closureCallsReceiverS(closure) {
		return []Diagnostic{{
			Rel:  rel,
			Line: line(closure.Body.Lbrace),
			Message: "Refresh's RunInTx closure must invoke at least one method on `s` — an empty " +
				"closure satisfies the wrap shape but does no work",
		}}, closure
	}

	return nil, closure
}

// closureCallsReceiverS reports whether the closure body contains at least
// one CallExpr whose callee chain is rooted at the receiver identifier `s`
// — accepting both direct method calls `s.foo(...)` and field-method
// chains `s.field.foo(...)` / `s.field.subfield.foo(...)`. Shape (3)
// requires the closure to do real work involving the service receiver.
func closureCallsReceiverS(closure *ast.FuncLit) bool {
	_, hasReceiverCall := FindFirstInSubtree[ast.CallExpr](closure.Body, func(call *ast.CallExpr) bool {
		return chainRootsAtIdent(call.Fun, "s")
	})
	return hasReceiverCall
}

// chainRootsAtIdent reports whether expr's selector / call chain bottoms
// out at *ast.Ident with the given name. Walks .X (SelectorExpr) and .Fun
// (CallExpr) links until a non-chain node or a terminal Ident is reached.
// Returns false on unrelated terminals (BasicLit, ParenExpr, etc.).
func chainRootsAtIdent(expr ast.Expr, name string) bool {
	for {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.CallExpr:
			expr = e.Fun
		case *ast.Ident:
			return e.Name == name
		default:
			return false
		}
	}
}

// scanGuardedCallsOutsideClosure walks fn.Body for CallExprs whose method
// resolves (via ResolveMethodCall) to a member of refreshGuardedMethods,
// emitting a Diagnostic when the call site sits outside the closure's
// lexical range. RevokeSessionDetached is intentionally absent from the
// guard map: PR #395 cascade paths must commit independently of the outer
// transaction (see godoc BS-2 in refresh_invariants_test.go).
func scanGuardedCallsOutsideClosure(p *Pass, fn *ast.FuncDecl, closure *ast.FuncLit, rel string) []Diagnostic {
	lbrace := closure.Body.Lbrace
	rbrace := closure.Body.Rbrace
	var out []Diagnostic

	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		// Skip calls inside the RunInTx closure — those are the desired location.
		if call.Pos() > lbrace && call.Pos() < rbrace {
			return
		}
		gm, banned := matchRefreshGuardedMethod(p.TypesInfo, call)
		if !banned {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"call to %s.%s.%s outside the RunInTx closure — move it inside the closure to "+
					"share the validate→update→rotate commit boundary",
				lastPkgSegment(gm.pkgPath), gm.typeName, gm.name,
			),
		})
	})
	return out
}

// matchRefreshGuardedMethod resolves call's callee via ResolveMethodCall and
// returns the guarded-method tuple plus a boolean indicating whether it is
// in refreshGuardedMethods.
//
// receiverNamedType (the *types.Named unwrap helper) is defined in
// sessionrefresh_no_session_create.go and shared package-locally.
func matchRefreshGuardedMethod(info *types.Info, call *ast.CallExpr) (refreshGuardedMethod, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return refreshGuardedMethod{}, false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn.Pkg() == nil {
		return refreshGuardedMethod{}, false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return refreshGuardedMethod{}, false
	}
	named, ok := receiverNamedType(sig.Recv().Type())
	if !ok {
		return refreshGuardedMethod{}, false
	}
	gm := refreshGuardedMethod{
		pkgPath:  fn.Pkg().Path(),
		typeName: named.Obj().Name(),
		name:     fn.Name(),
	}
	_, banned := refreshGuardedMethods[gm]
	return gm, banned
}

// lastPkgSegment returns the substring after the final '/' — the package's
// natural short name. Used solely for diagnostic-message brevity (no
// semantic load).
func lastPkgSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isTxRunnerRunInTxCall reports whether call is `s.txRunner.RunInTx(...)`.
// Pure AST check — the structural anchor that locates the transaction
// boundary; receiver-type resolution is not required because every Refresh
// implementation in scope uses this exact selector chain.
func isTxRunnerRunInTxCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RunInTx" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "txRunner" {
		return false
	}
	ident, ok := inner.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "s"
}

// resolveClosureArg returns the *ast.FuncLit that arg refers to: either arg
// itself (inline closure) or the FuncLit bound to the identifier in body.
// When the identifier has multiple FuncLit assignments (`do := func(){...};
// do = func(){...}`), the LAST assignment wins — that is the value of `do`
// at the point of the RunInTx call site, matching Go's evaluation semantics.
// Returns nil if neither pattern matches.
func resolveClosureArg(body *ast.BlockStmt, arg ast.Expr) *ast.FuncLit {
	if fl, ok := arg.(*ast.FuncLit); ok {
		return fl
	}
	ident, ok := arg.(*ast.Ident)
	if !ok {
		return nil
	}
	var lastAssigned *ast.FuncLit
	EachInSubtree[ast.AssignStmt](body, func(assign *ast.AssignStmt) {
		lastAssigned = applyAssignToClosureVar(assign, ident.Name, lastAssigned)
	})
	return lastAssigned
}

// applyAssignToClosureVar inspects a single AssignStmt for an assignment to
// the variable named varName. Returns the updated *ast.FuncLit result:
//   - if the assignment's RHS at the matched position is a FuncLit, returns it
//   - if the assignment's RHS is non-FuncLit, returns nil (resets the tracked value)
//   - if varName is not on the LHS, returns prev unchanged
func applyAssignToClosureVar(assign *ast.AssignStmt, varName string, prev *ast.FuncLit) *ast.FuncLit {
	lhsIndex := buildLhsIndex(assign)
	for id, i := range lhsIndex {
		if id.Name != varName {
			continue
		}
		if i >= len(assign.Rhs) {
			continue
		}
		fl, ok := assign.Rhs[i].(*ast.FuncLit)
		if !ok {
			// Non-FuncLit assignment — overrides any prior FuncLit.
			return nil
		}
		return fl
	}
	return prev
}

// buildLhsIndex constructs a map from each *ast.Ident in assign.Lhs to its
// position index. Used by applyAssignToClosureVar to locate a named variable
// on the left-hand side of an assignment statement.
func buildLhsIndex(assign *ast.AssignStmt) map[*ast.Ident]int {
	lhsIndex := make(map[*ast.Ident]int, len(assign.Lhs))
	EachInSubtree[ast.Ident](assign, func(id *ast.Ident) {
		for i, lhs := range assign.Lhs {
			if lhs == id {
				lhsIndex[id] = i
				break
			}
		}
	})
	return lhsIndex
}

// refreshReceiverName extracts the receiver variable's name from a method
// FuncDecl. Returns "" when the receiver is anonymous (`func (*Service)
// Refresh(...)`) which would also bypass the rule's `s`-rooted anchors.
func refreshReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return ""
	}
	if len(fn.Recv.List[0].Names) != 1 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// ─── REFRESH-INVALID-INDEX-SINGLE-SOURCE-01 ──────────────────────────────────

// CheckRefreshInvalidIndexSingleSource01 runs REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
// over the running module and returns its diagnostics. GoCell's own
// TestRefreshInvalidIndexSingleSource01 calls it directly — single source.
func CheckRefreshInvalidIndexSingleSource01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	type declarationSite struct {
		rel  string
		line int
	}
	var declarations []declarationSite

	scope := ModuleScope(root)
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Name.Name != "DetectInvalidIndexes" {
					return
				}
				if fd.Recv != nil {
					return
				}
				pos := p.Fset.Position(fd.Pos())
				declarations = append(declarations, declarationSite{
					rel:  filepath.ToSlash(p.Rel(file)),
					line: pos.Line,
				})
			})
		}
		return nil
	})

	var diags []Diagnostic
	if len(declarations) == 0 {
		diags = append(diags, Diagnostic{
			Rel:  canonicalInvalidIndexFile,
			Line: 0,
			Message: fmt.Sprintf("DetectInvalidIndexes not declared anywhere — expected it in %s",
				canonicalInvalidIndexFile),
		})
		return diags
	}
	if len(declarations) > 1 {
		for _, d := range declarations {
			diags = append(diags, Diagnostic{
				Rel:  d.rel,
				Line: d.line,
				Message: fmt.Sprintf("DetectInvalidIndexes declared in %d files (expected 1)",
					len(declarations)),
			})
		}
		return diags
	}
	if declarations[0].rel != canonicalInvalidIndexFile {
		diags = append(diags, Diagnostic{
			Rel:  declarations[0].rel,
			Line: declarations[0].line,
			Message: fmt.Sprintf("DetectInvalidIndexes must be declared in %s, not %s",
				canonicalInvalidIndexFile, declarations[0].rel),
		})
	}
	return diags
}

// ─── REFRESH-AMBIENT-TX-01 ────────────────────────────────────────────────────

// CheckRefreshAmbientTX01 runs REFRESH-AMBIENT-TX-01 over the running module
// and returns its diagnostics. GoCell's own TestRefreshAmbientTX01 calls it
// directly — single source.
func CheckRefreshAmbientTX01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	const rel = "adapters/postgres/refresh_store.go"
	root := findModuleRoot(t)

	scope := DirsScope(
		root, []string{filepath.Dir(rel)},
		MatchRels(func(r string) bool { return r == rel }),
	)

	var (
		diags     []Diagnostic
		foundFile bool
	)

	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			if p.Rel(file) != rel {
				continue
			}
			foundFile = true
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Begin" {
					return
				}
				pos := p.Fset.Position(call.Pos())
				diags = append(diags, Diagnostic{
					Rel:     rel,
					Line:    pos.Line,
					Message: ".Begin() call — refresh_store must delegate to TxRunner, not acquire transactions directly",
				})
			})
		}
		return nil
	})

	if !foundFile {
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    0,
			Message: "file not found",
		})
	}
	return diags
}
