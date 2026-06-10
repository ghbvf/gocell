//go:build archtest

// refresh_invariants_test.go consolidates refresh-theme invariants:
//   - INVARIANT: REFRESH-CROSS-STORE-TX-01
//   - INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//   - INVARIANT: REFRESH-AMBIENT-TX-01

package archtest

// Detector logic and Check* functions live in refresh_invariants.go (non-test)
// so they can be compiled by external Cell repositories. This file is the thin
// dogfood wrapper that calls Check* and the blind-spot / meta tests that remain
// test-only.

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

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
// # AI-robust: Medium (type-aware)
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
//     scope per ai-robust.md §3 (no Go static rule reaches it).
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
	diags := CheckRefreshCrossStoreTX01(t, ConfigForExternalCell{})
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
// AI-robust: Soft (string-anchor on production AST), but exists precisely
// to make BS-1 fail-loud rather than fail-silent — the章程级 minimum for
// any blind-spot disclosure per ai-robust.md "盲区 + 反向自检测试".
func TestRefreshCrossStoreTX01_BlindSpot_ServiceRefreshReceiverIsS(t *testing.T) {
	diags := Run(t, Typed(
		TypedOpts{Tests: false},
		[]string{"./cells/accesscore/slices/sessionrefresh/..."},
	),
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
							name,
						),
					})
				})
			}
			return out
		})

	Report(t, ruleRefreshCrossStoreTX01+"-BS-1", diags)
}

// INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//
// refresh_invalid_index_single_source_test.go enforces REFRESH-INVALID-INDEX-SINGLE-SOURCE-01:
// the function "DetectInvalidIndexes" must be declared (defined) in exactly one
// production (non-_test.go) Go file across the entire repository:
// adapters/postgres/schema_guard.go.
//
// Callers of DetectInvalidIndexes (e.g. migrator.go, cellmodules/configcore/storage.go)
// are allowed. Only a second *declaration* (func DetectInvalidIndexes ...) would
// violate the rule, which would indicate B8 or future work introducing a
// parallel invalid-index check path outside schema_guard.
func TestRefreshInvalidIndexSingleSource01(t *testing.T) {
	diags := CheckRefreshInvalidIndexSingleSource01(t, ConfigForExternalCell{})
	Report(t, ruleRefreshInvalidIndexSingleSource01, diags)
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
	diags := CheckRefreshAmbientTX01(t, ConfigForExternalCell{})
	Report(t, ruleRefreshAmbientTX01, diags)
}
