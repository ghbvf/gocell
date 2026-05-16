// invariants:
//   - INVARIANT: SESSION-PROTOCOL-COMPOSITION-ROOT-01
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	sessionProtocolRuleID = "SESSION-PROTOCOL-COMPOSITION-ROOT-01"
	sessionPkgImportPath  = "github.com/ghbvf/gocell/runtime/auth/session"
)

// sessionProtocolForbidden is the closed set of session-package constructors
// banned outside the composition root. Both forms (error-returning + panic-
// wrapping) belong to wiring authority, not consumer Cell code.
var sessionProtocolForbidden = map[string]struct{}{
	"NewProtocol":     {},
	"MustNewProtocol": {},
}

// TestSessionProtocol_CompositionRootOnly enforces
// SESSION-PROTOCOL-COMPOSITION-ROOT-01: session.NewProtocol /
// session.MustNewProtocol may only be invoked from cmd/* (composition root)
// or runtime/auth/session/* (the package itself + storetest helpers). Cells,
// runtime/* (non-session), adapters/*, and tests outside session/* must
// receive an injected *session.Protocol — not construct one.
//
// # AI-rebust: Medium (type-aware)
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
// RunTyped(opts.Tests=false) loads only production-variant packages, so
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
//     scope per ai-collab.md §3 (no Go static rule reaches it).
func TestSessionProtocol_CompositionRootOnly(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		sessionProtocolProductionPatterns(),
		scanSessionProtocolViolations,
	)
	Report(t, sessionProtocolRuleID, diags)
}

// sessionProtocolProductionPatterns returns the package patterns scanned by
// the production rule (cells / runtime / adapters).
//
// cmd/ and examples/ are intentionally outside scope:
//
//   - cmd/* is the composition root by definition — wiring authority owns
//     session.NewProtocol construction.
//   - examples/* each carry their own composition root (typically
//     examples/<demo>/main.go or app.go); allowing them mirrors the
//     AUTH-PLAN-04 / LAYER-09 carve-out for example projects. The rule
//     does not validate whether examples/* actually use NewProtocol
//     legitimately — examples are intentionally a separate enforcement
//     surface, owned by their own composition-root files.
//
// Adding a new module subtree that owns a composition root (e.g. a future
// tools/<demo>/) requires extending this list AND updating the godoc
// above so the carve-out is documented at every layer.
func sessionProtocolProductionPatterns() []string {
	return []string{
		"./cells/...",
		"./runtime/...",
		"./adapters/...",
	}
}

// scanSessionProtocolViolations walks every CallExpr in pass.Files, resolves
// the callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and
// flags hits whose owning package is runtime/auth/session and whose name is in
// sessionProtocolForbidden.
//
// Two file-level filters apply:
//
//   - _test.go suffix: defense-in-depth alongside TypedOpts{Tests: false}.
//   - rel under "runtime/auth/session/": the package itself owns the
//     constructor; subpackages (storetest, etc.) are part of the wiring
//     authority and need NewProtocol for fake construction.
//
// Used by both TestSessionProtocol_CompositionRootOnly (production scan,
// asserts zero diagnostics) and TestSessionProtocol_RedFixtureDetected
// (fixture scan, asserts ≥ 6 diagnostics across qualified / aliased / dot
// import shapes × NewProtocol + MustNewProtocol).
func scanSessionProtocolViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if strings.HasPrefix(rel, "runtime/auth/session/") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok || pkgPath != sessionPkgImportPath {
				return
			}
			if _, banned := sessionProtocolForbidden[name]; !banned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"session.%s must only be called from cmd/* (composition root) or runtime/auth/session/*; "+
						"cells / runtime (non-session) / adapters must consume an injected *session.Protocol",
					name),
			})
		})
	}
	return out
}
