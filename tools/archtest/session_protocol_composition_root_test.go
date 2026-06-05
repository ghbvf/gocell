// invariants:
//   - INVARIANT: SESSION-PROTOCOL-COMPOSITION-ROOT-01
package archtest

import (
	"testing"
)

// TestSessionProtocol_CompositionRootOnly enforces
// SESSION-PROTOCOL-COMPOSITION-ROOT-01: session.NewProtocol /
// session.MustNewProtocol may only be invoked from cmd/* (composition root)
// or runtime/auth/session/* (the package itself + storetest helpers). Cells,
// runtime/* (non-session), adapters/*, and tests outside session/* must
// receive an injected *session.Protocol — not construct one.
//
// # AI-robust: Medium (type-aware)
//
// T3 Wave 2 upgrade (docs/plans/202605082145-034-pg-corecell-b-route-plan.md
// §S4c T3, FU-3b 闭环): the rule resolves every CallExpr's callee through
// archtest.ResolvePackageRef, matching by the callee's owning package import
// path rather than the source-level Ident name. The Soft predecessor matched
// `pkg.Name == "session"` on raw AST and missed two bypass forms:
//
//   - aliased import:  `import sess "…/session"; sess.NewProtocol(...)`
//   - dot import:      `import . "…/session"; NewProtocol(...)`
//
// archtest.ResolvePackageRef covers all three callee shapes in one resolution:
//   - SelectorExpr `pkg.Func` / `alias.Func` → info.Uses[sel.X].(*types.PkgName)
//   - bare Ident `Func` (dot-import)          → info.Uses[id].(*types.Func)
//
// Hard is architecturally unattainable for this rule's shape (caller-allowlist
// across multiple non-nested roots — cmd/, runtime/auth/session/, examples/):
// Go's internal/ package mechanic admits only a single owning subtree, so
// NewProtocol cannot be sealed to permit cmd/ + session/* simultaneously
// without reshaping the typed Protocol injection paradigm (defeats S2 / K-04
// decisions). See plan §S4c T3 reflection L3 for the full Hard analysis.
//
// # _test.go scope
//
// Run(t, Typed(TypedOpts{Tests: false}, ...)) loads only production-variant packages, so
// _test.go files are not in pass.Files. The rule additionally filters by
// rel suffix for clarity — both gates are conservative and align with the
// SESSIONREFRESH-NO-SESSION-CREATE-01 convention.
//
// # 盲区 (BS)
//
//   - BS-1 Name shadowing: a non-session package with a top-level function
//     literally named NewProtocol/MustNewProtocol does NOT match (pkgPath
//     comparison rejects it). archtest.ResolvePackageRef returns the
//     callee's owning package via go/types, not the import-site alias.
//   - BS-2 Function-value indirection: `var fn = session.NewProtocol; fn()`
//     — the `session.NewProtocol` reference itself is a SelectorExpr that
//     ResolvePackageRef would match if it were a CallExpr.Fun. But the
//     subsequent `fn()` callee is a *types.Var (not *types.Func), so the
//     resolver returns ok=false. Accepted: the repo has no such pattern;
//     PASS-FUNNEL-RESOLVE-01 fixture-side blind-spot coverage already
//     anchors this resolver behavior (typeseval call_target_test.go).
//   - BS-3 Reflection construction (reflect.New + MethodByName): out of
//     scope per ai-robust.md §3 (no Go static rule reaches it).
func TestSessionProtocol_CompositionRootOnly(t *testing.T) {
	t.Parallel()
	Report(t, ruleSessionProtocolCompositionRoot01, CheckSessionProtocolCompositionRoot01(t, ConfigForExternalCell{}))
}
