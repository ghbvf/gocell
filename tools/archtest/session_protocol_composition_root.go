package archtest

// session_protocol_composition_root.go — importable rule logic for
// SESSION-PROTOCOL-COMPOSITION-ROOT-01 (#1302 M3).
//
// Detection logic is in this non-test file so it can be compiled by external
// Cell repositories (Go never compiles a dependency's _test.go). GoCell's own
// TestSessionProtocol_CompositionRootOnly in
// session_protocol_composition_root_test.go calls the same Check* function —
// single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath] (no bare literals).
// The scan SCOPE is the running module, supplied by the driver. See external.go.

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

// ─── rule ID constant ─────────────────────────────────────────────────────────

const ruleSessionProtocolCompositionRoot01 = "SESSION-PROTOCOL-COMPOSITION-ROOT-01"

// ─── forbidden constructor set ────────────────────────────────────────────────

// sessionProtocolForbidden is the closed set of session-package constructors
// banned outside the composition root. (MustNewProtocol was deleted by
// B2-K-02; only NewProtocol remains.)
var sessionProtocolForbidden = map[string]struct{}{
	"NewProtocol": {},
}

// ─── SESSION-PROTOCOL-COMPOSITION-ROOT-01 ────────────────────────────────────

// CheckSessionProtocolCompositionRoot01 runs SESSION-PROTOCOL-COMPOSITION-ROOT-01
// over the running module and returns its diagnostics.
//
// session.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/auth/session/* (the package itself + storetest helpers). Cells,
// runtime/* (non-session), adapters/*, and tests outside session/* must receive
// an injected *session.Protocol — not construct one.
//
// cmd/ and examples/ are intentionally outside the scan scope:
//   - cmd/* is the composition root by definition — wiring authority owns
//     session.NewProtocol construction.
//   - examples/* each carry their own composition root; allowing them mirrors
//     the AUTH-PLAN-04 / LAYER-09 carve-out for example projects.
func CheckSessionProtocolCompositionRoot01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(
		TypedOpts{Tests: false},
		sessionProtocolProductionPatterns(),
	), scanSessionProtocolViolations)
}

// sessionProtocolProductionPatterns returns the package patterns scanned by
// the production rule (cells / runtime / adapters).
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
			if !ok || pkgPath != sessionStorePkg { // sessionStorePkg declared in credential_invalidate_funnel_invariants.go
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
					name,
				),
			})
		})
	}
	return out
}
