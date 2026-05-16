// refresh_invariants_test.go consolidates refresh-theme invariants:
//   - INVARIANT: REFRESH-CROSS-STORE-TX-01
//   - INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//   - INVARIANT: REFRESH-AMBIENT-TX-01

package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleRefreshCrossStoreTX01 = "REFRESH-CROSS-STORE-TX-01"

// canonicalInvalidIndexFile is the only file allowed to define DetectInvalidIndexes.
const canonicalInvalidIndexFile = "adapters/postgres/schema_guard.go"

const ruleRefreshInvalidIndexSingleSource01 = "REFRESH-INVALID-INDEX-SINGLE-SOURCE-01"

const ruleRefreshAmbientTX01 = "REFRESH-AMBIENT-TX-01"

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
	{pkgPath: "github.com/ghbvf/gocell/runtime/auth/refresh", typeName: "Store", name: "Peek"}:                        {},
	{pkgPath: "github.com/ghbvf/gocell/runtime/auth/refresh", typeName: "Store", name: "Rotate"}:                      {},
	{pkgPath: "github.com/ghbvf/gocell/runtime/auth/refresh", typeName: "Store", name: "RevokeSession"}:               {},
	{pkgPath: "github.com/ghbvf/gocell/runtime/auth/session", typeName: "Store", name: "Get"}:                         {},
	{pkgPath: "github.com/ghbvf/gocell/cells/accesscore/internal/ports", typeName: "UserRepository", name: "GetByID"}: {},
}

// INVARIANT: REFRESH-CROSS-STORE-TX-01
//
// cells/accesscore/slices/sessionrefresh.Service.Refresh must wrap the
// validate → update → rotate lookup chain in a single s.txRunner.RunInTx
// call so that PG refresh-store, PG session-store, and PG user-store reads
// share one commit boundary. The rule fires on four shape constraints:
//
//  1. Refresh body must contain exactly one s.txRunner.RunInTx(...) call.
//  2. The RunInTx call's second argument resolves to a *ast.FuncLit (either
//     inline `func(...) error { ... }` or an identifier bound to one).
//  3. The closure body must invoke at least one method on `s` — guards
//     against an empty/no-op wrap that satisfies (1) without doing work.
//  4. Each guarded method call (see refreshGuardedMethods) must reside
//     inside the closure. The check is type-aware: it resolves every
//     CallExpr's method through archtest.ResolveMethodCall and matches on
//     (pkgPath, namedReceiverType, methodName) — not on the syntactic
//     field name. A future rename of s.sessionStore to s.sStore would not
//     bypass the rule.
//
// # AI-rebust: Medium (type-aware)
//
// T3 Wave 2 upgrade (S4c plan §T3 FU-3b 闭环): the Soft predecessor used a
// hand-coded "s.<field>.<method>" Ident-name match with a stale guard set
// (sessionRepo.Update / sessionRepo.GetByID — deleted by PR #482 along with
// cell-private SessionRepository). The Medium upgrade:
//
//   - Updates the guard set to the post-PR #482 lookup chain (adds
//     sessionStore.Get; drops sessionRepo.* stale entries).
//   - Uses archtest.ResolveMethodCall (info.Selections) to identify
//     methods by their owning interface, eliminating the field-name match.
//
// Hard is architecturally unattainable for this rule's shape: it asserts
// "call X must lexically reside inside closure Y" — a lexical-position
// constraint that no Go type-system or codegen funnel can express. Medium
// (type-aware archtest) is the章程级 ceiling for structural rules of this
// form (see plan §S4c T3 reflection L3).
//
// # Scope: only direct Refresh.Body calls are inspected
//
// The rule scans CallExprs in *Refresh's own body* — not inside helper
// methods that Refresh transitively calls. Production code intentionally
// uses this property: Refresh's RunInTx closure body is `pair, err =
// s.refreshInTx(txCtx, outerCtx, refreshToken); return err`, and the
// guarded calls (Peek / sessionStore.Get / userRepo.GetByID / Rotate)
// live inside the named helper `refreshInTx`. Helper-resident calls are
// "inside the wrap" by transitive reachability (the helper executes
// inside the closure on the call stack); the original Soft predecessor
// adopted this same scope (its godoc: "They may live inside the closure
// or in any helper method on s reachable through it — any direct call
// from Refresh's top level escapes the wrap").
//
// Practical implication: the rule defends against a developer adding a
// new guarded call DIRECTLY at Refresh's top level outside the closure
// (an easy-to-miss regression). It does NOT enforce that helpers like
// refreshInTx commit their own guarded calls inside the closure — a
// helper that opens its own ambient tx would escape detection. Treating
// transitive enforcement as out-of-scope is intentional: helper
// extraction is a normal refactor; widening the rule to follow the call
// graph would require fixed-point analysis and produce false positives
// when helpers branch.
//
// # 盲区 (BS)
//
//   - BS-1 Method receiver renamed from `s`: the structural anchors
//     isTxRunnerRunInTxCall + isServiceRefreshMethod + closureCallsReceiverS
//     all literally match `ident.Name == "s"` / `"Service"` / `"txRunner"`.
//     A renamed receiver (e.g. `func (svc *Service) Refresh(...)`) would
//     bypass these checks. Reverse self-check:
//     TestRefreshCrossStoreTX01_BlindSpot_ServiceRefreshReceiverIsS asserts
//     production Service.Refresh's receiver name is literally "s" so a
//     future rename surfaces as a self-check failure before the rule
//     silently disengages.
//   - BS-2 Detached cascade variant (RevokeSessionDetached) is intentionally
//     out of scope per PR #395: detached paths commit independently of the
//     outer tx. Listed in this godoc so future maintainers see it is a
//     known carve-out, not an oversight.
//   - BS-3 Reflection / dynamic dispatch on a refresh.Store value — out of
//     scope per ai-collab.md §3 (no Go static rule reaches it).
//   - BS-4 RED fixture cannot exercise the (ports, UserRepository, GetByID)
//     guard entry: cells/accesscore/internal/ports is internal-importable
//     only within cells/accesscore/ and is not reachable from the fixture
//     at tools/archtest/internal/refreshinvariantsfixture/. The
//     ResolveMethodCall resolution path is structurally identical to the
//     (refresh, Store, *) entries (which ARE exercised by the fixture),
//     so the resolver's correctness on the userRepo path is covered by
//     analogy; the only un-tested layer is the literal pkgPath constant
//     for `cells/accesscore/internal/ports`. Accepted limitation.
//   - BS-5 isTxRunnerRunInTxCall is a Soft anchor (pure AST string match
//     on `s.txRunner.RunInTx`), not type-aware. If the field name
//     `txRunner` is renamed (e.g. to `tx`), the rule's RunInTx detection
//     silently fails. The receiver-name reverse self-check above (BS-1)
//     happens to also exercise the txRunner.RunInTx call site, so a
//     field rename would surface there. The rule's Medium grade comes
//     from the guarded-method match (ResolveMethodCall); this anchor is
//     a Soft assist, honestly disclosed here.
func TestRefreshCrossStoreTX01(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		[]string{"./cells/accesscore/slices/sessionrefresh/..."},
		scanRefreshCrossStoreTX,
	)
	Report(t, ruleRefreshCrossStoreTX01, diags)
}

// TestRefreshCrossStoreTX01_BlindSpot_ServiceRefreshReceiverIsS is the
// reverse self-check for BS-1. The four structural anchors in
// TestRefreshCrossStoreTX01 all depend on the receiver name `s`:
//
//   - isTxRunnerRunInTxCall looks for `s.txRunner.RunInTx(...)`
//   - isServiceRefreshMethod accepts any (*Service).Refresh / (Service).Refresh
//   - closureCallsReceiverS verifies ≥ 1 call rooted at Ident `s` in the closure
//   - scanGuardedCallsOutsideClosure matches CallExpr selectors via
//     ResolveMethodCall (receiver-type independent, but the structural
//     prerequisites above gate the analysis)
//
// If a future refactor renames the receiver from `s` to `svc` / `r` / `c`,
// TestRefreshCrossStoreTX01 silently disengages: the structural anchors
// all return false, the rule reports zero diagnostics, and a real bug
// (a guarded call escaped to Refresh's top level) would slip through.
// This test fails loudly when the convention drifts so the rule's
// authors are forced to update the anchors in lock-step.
//
// AI-rebust: Soft (string-anchor on production AST), but exists precisely
// to make BS-1 fail-loud rather than fail-silent — the章程级 minimum for
// any blind-spot disclosure per ai-collab.md "盲区 + 反向自检测试".
func TestRefreshCrossStoreTX01_BlindSpot_ServiceRefreshReceiverIsS(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		[]string{"./cells/accesscore/slices/sessionrefresh/..."},
		func(p *Pass) []Diagnostic {
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
					name := refreshReceiverName(fn)
					if name == "s" {
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Pos()).Line,
						Message: fmt.Sprintf(
							"(*Service).Refresh receiver is %q; rule structural anchors hard-code "+
								"`s` — update isTxRunnerRunInTxCall + closureCallsReceiverS + the "+
								"rule godoc (BS-1) in lock-step before renaming the receiver",
							name),
					})
				})
			}
			return out
		})
	Report(t, ruleRefreshCrossStoreTX01+"-BS-1", diags)
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

// scanRefreshCrossStoreTX walks every production file in pass.Files for a
// (*Service).Refresh method, then applies the four-shape check (see godoc
// above) to each one. Reused by TestRefreshCrossStoreTX01_RedFixtureDetected
// against the fixture package; both call sites must use the same scan
// function so the fixture proves the live rule pipeline, not a parallel
// implementation.
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
// TestRefreshCrossStoreTX01. It returns one Diagnostic per shape violation
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
//
// Pre-T3 the check required a direct `s.<method>(...)` call (sel.X must
// be Ident "s"). Production Refresh happened to satisfy this with
// s.verifySession / s.fetchUserForRefresh, but the strict shape rejected
// any closure that only used field-method chains. The relaxation matches
// the original intent ("closure does work involving s") without changing
// production diagnostics — production still emits a non-empty hasReceiverCall.
func closureCallsReceiverS(closure *ast.FuncLit) bool {
	var hasReceiverCall bool
	EachInSubtree[ast.CallExpr](closure.Body, func(call *ast.CallExpr) {
		if hasReceiverCall {
			return
		}
		if chainRootsAtIdent(call.Fun, "s") {
			hasReceiverCall = true
		}
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
// transaction (see godoc BS-2).
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
				lastPkgSegment(gm.pkgPath), gm.typeName, gm.name),
		})
	})
	return out
}

// matchRefreshGuardedMethod resolves call's callee via ResolveMethodCall and
// returns the guarded-method tuple plus a boolean indicating whether it is
// in refreshGuardedMethods.
//
// receiverNamedType (the *types.Named unwrap helper) is defined in
// sessionrefresh_no_session_create_test.go and shared package-locally.
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
// Reasoning about only the first assignment would let an attacker satisfy
// the structural guard with a non-trivial first FuncLit and reassign a
// no-op FuncLit before passing `do` to RunInTx.
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
		// Build an index map from Ident pointer to position in Lhs.
		lhsIndex := make(map[*ast.Ident]int, len(assign.Lhs))
		EachInSubtree[ast.Ident](assign, func(id *ast.Ident) {
			for i, lhs := range assign.Lhs {
				if lhs == id {
					lhsIndex[id] = i
					break
				}
			}
		})
		for id, i := range lhsIndex {
			if id.Name != ident.Name {
				continue
			}
			if i >= len(assign.Rhs) {
				continue
			}
			fl, ok := assign.Rhs[i].(*ast.FuncLit)
			if !ok {
				// Non-FuncLit assignment to the identifier (e.g. another
				// variable, a function call, or nil) — it overrides any
				// prior FuncLit. Reset so we don't claim the previous one.
				lastAssigned = nil
				continue
			}
			lastAssigned = fl
		}
	})
	return lastAssigned
}

// INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//
// refresh_invalid_index_single_source_test.go enforces REFRESH-INVALID-INDEX-SINGLE-SOURCE-01:
// the function "DetectInvalidIndexes" must be declared (defined) in exactly one
// production (non-_test.go) Go file across the entire repository:
// adapters/postgres/schema_guard.go.
//
// Callers of DetectInvalidIndexes (e.g. migrator.go, cmd/corebundle/bundle_configcore_storage.go)
// are allowed. Only a second *declaration* (func DetectInvalidIndexes ...) would
// violate the rule, which would indicate B8 or future work introducing a
// parallel invalid-index check path outside schema_guard.
func TestRefreshInvalidIndexSingleSource01(t *testing.T) {
	root := findModuleRoot(t)

	type declarationSite struct {
		rel  string
		line int
	}
	var declarations []declarationSite

	scope := ModuleScope(root)
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Name.Name != "DetectInvalidIndexes" {
					return
				}
				// Only top-level function declarations (no receiver).
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

	if len(declarations) == 0 {
		t.Fatalf("%s: DetectInvalidIndexes not declared anywhere — expected it in %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile)
	}

	if len(declarations) > 1 {
		t.Logf("%s: DetectInvalidIndexes declared in %d files (expected 1):", ruleRefreshInvalidIndexSingleSource01, len(declarations))
		for _, d := range declarations {
			t.Logf("  %s:%d", d.rel, d.line)
		}
	}

	assert.Len(t, declarations, 1,
		"%s: DetectInvalidIndexes must be declared in exactly one production file (%s); "+
			"found declarations in %d files — callers are allowed, new parallel definitions are not",
		ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, len(declarations))

	if len(declarations) == 1 {
		assert.Equal(t, canonicalInvalidIndexFile, declarations[0].rel,
			"%s: DetectInvalidIndexes must be declared in %s, not %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, declarations[0].rel)
	}
}

// INVARIANT: REFRESH-AMBIENT-TX-01
//
// refresh_store_ambient_tx_test.go enforces REFRESH-AMBIENT-TX-01:
// adapters/postgres/refresh_store.go must not contain any direct pool.Begin /
// (*pgxpool.Pool).Begin / tx.Begin calls. After B2-A-08, Peek and Rotate
// delegate transaction management to the injected TxRunner; the store itself
// must not acquire transactions directly.
//
// The rule scans the AST for SelectorExpr calls whose Sel.Name is "Begin"
// where the receiver is a known pool-like identifier. It also catches bare
// method calls named "Begin" on any expression, since the only legitimate
// Begin callers in refresh_store.go would be pool or tx variables.
func TestRefreshAmbientTX01(t *testing.T) {
	const rel = "adapters/postgres/refresh_store.go"
	root := findModuleRoot(t)

	scope := DirsScope(root, []string{filepath.Dir(rel)},
		MatchRels(func(r string) bool { return r == rel }),
	)

	type violation struct {
		line int
		expr string
	}
	var (
		violations []violation
		foundFile  bool
	)

	Run(t, scope, func(p *Pass) []Diagnostic {
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
				violations = append(violations, violation{
					line: pos.Line,
					expr: fmt.Sprintf("call to .Begin() at line %d", pos.Line),
				})
			})
		}
		return nil
	})

	require.True(t, foundFile, "%s: file not found: %s", ruleRefreshAmbientTX01, rel)

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s) in %s:", ruleRefreshAmbientTX01, len(violations), rel)
		for _, v := range violations {
			t.Logf("  line %d: .Begin() call — refresh_store must delegate to TxRunner, not acquire transactions directly", v.line)
		}
	}
	assert.Empty(t, violations,
		"%s: %s must not contain .Begin() calls; use injected TxRunner.RunInTx instead (B2-A-08)",
		ruleRefreshAmbientTX01, rel)
}
