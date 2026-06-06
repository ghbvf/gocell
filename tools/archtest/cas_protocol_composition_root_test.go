// INVARIANT: CAS-PROTOCOL-COMPOSITION-ROOT-01
//
// # CAS-PROTOCOL-COMPOSITION-ROOT-01
//
// cas.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/state/cas/* (the package itself). Cells, runtime/* (non-cas), and
// adapters/* must receive an injected *cas.Protocol — not construct one.
//
// # AI-robust: Medium (type-aware)
//
// The rule resolves every CallExpr's callee through archtest.ResolvePackageRef,
// matching by the callee's owning package import path rather than the source-level
// Ident name. The Soft predecessor matched `sel.X.(*ast.Ident).Name == "cas"` on
// raw AST and missed two bypass forms:
//
//   - aliased import:  `import casPkg "…/runtime/state/cas"; casPkg.NewProtocol(...)`
//   - dot import:      `import . "…/runtime/state/cas"; NewProtocol(...)`
//
// archtest.ResolvePackageRef covers all three callee shapes in one resolution:
//   - SelectorExpr `pkg.Func` / `alias.Func` → info.Uses[sel.X].(*types.PkgName)
//   - bare Ident `Func` (dot-import)          → info.Uses[id].(*types.Func)
//
// Hard is architecturally unattainable for this rule's shape (caller-allowlist
// across multiple non-nested roots — cmd/, runtime/state/cas/, examples/):
// Go's internal/ package mechanic admits only a single owning subtree, so
// NewProtocol cannot be sealed to permit cmd/ + runtime/state/cas/* simultaneously
// without reshaping the typed Protocol injection paradigm. See SESSION-PROTOCOL-
// COMPOSITION-ROOT-01 for the same Hard-barrier analysis.
//
// # _test.go scope
//
// Run(t, Typed(TypedOpts{Tests: false}, ...)) loads only production-variant packages, so
// _test.go files are not in pass.Files. The scanner additionally filters by
// rel suffix for clarity — both gates are conservative.
//
// # Blind spots (BS)
//
//   - BS-1 Name shadowing: a non-cas package with a top-level function literally
//     named NewProtocol does NOT match (pkgPath comparison rejects it).
//     archtest.ResolvePackageRef returns the callee's owning package via
//     go/types, not the import-site alias.
//
//   - BS-2 Function-value indirection: `var fn = cas.NewProtocol; fn(...)` —
//     the `cas.NewProtocol` reference itself is a SelectorExpr that
//     ResolvePackageRef would match if it were a CallExpr.Fun. But the subsequent
//     `fn()` callee is a *types.Var (not *types.Func), so the resolver returns
//     ok=false. Accepted: the repo has no such pattern; this is the same accepted
//     blind spot as SESSION-PROTOCOL-COMPOSITION-ROOT-01 BS-2.
//
//   - BS-3 Reflection construction: out of scope per ai-robust.md §3.
package archtest

import (
	"testing"
)

// TestCASProtocol_CompositionRootOnly enforces CAS-PROTOCOL-COMPOSITION-ROOT-01:
// cas.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/state/cas/* (the package itself). Cells, runtime/* (non-cas), and
// adapters/* must consume an injected *cas.Protocol — not construct one.
//
// cmd/ and examples/ are intentionally outside the scan scope:
//   - cmd/* is the composition root by definition.
//   - examples/* each carry their own composition root; allowing them mirrors
//     the AUTH-PLAN-04 / LAYER-09 carve-out for example projects.
func TestCASProtocol_CompositionRootOnly(t *testing.T) {
	t.Parallel()
	Report(t, ruleCASProtocolCompositionRoot01, CheckCASProtocolCompositionRoot01(t, ConfigForExternalCell{}))
}
