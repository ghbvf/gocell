package archtest

// cas_protocol_composition_root.go — importable rule logic for
// CAS-PROTOCOL-COMPOSITION-ROOT-01 (#1302 M3).
//
// Detection logic is in this non-test file so it can be compiled by external
// Cell repositories (Go never compiles a dependency's _test.go). GoCell's own
// TestCASProtocol_CompositionRootOnly in
// cas_protocol_composition_root_test.go calls the same Check* function —
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

const ruleCASProtocolCompositionRoot01 = "CAS-PROTOCOL-COMPOSITION-ROOT-01"

// ─── platform-symbol path constants ──────────────────────────────────────────

// casPkgPath is the canonical import path of the runtime/state/cas package,
// derived from PlatformModulePath — no bare literal.
const casPkgPath = PlatformModulePath + "/runtime/state/cas"

// casOwnPkgPrefix and casOwnPkgExact are the relative-path guards used to
// exempt the cas package itself and its sub-packages from the rule.
const (
	casOwnPkgPrefix = "runtime/state/cas/"
	casOwnPkgExact  = "runtime/state/cas"
)

// ─── forbidden constructor set ────────────────────────────────────────────────

// casProtocolForbidden is the closed set of cas-package constructors banned
// outside the composition root. (MustNewProtocol was deleted by B2-K-02;
// only NewProtocol remains.)
var casProtocolForbidden = map[string]struct{}{
	"NewProtocol": {},
}

// ─── CAS-PROTOCOL-COMPOSITION-ROOT-01 ────────────────────────────────────────

// CheckCASProtocolCompositionRoot01 runs CAS-PROTOCOL-COMPOSITION-ROOT-01
// over the running module and returns its diagnostics.
//
// cas.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/state/cas/* (the package itself). Cells, runtime/* (non-cas), and
// adapters/* must receive an injected *cas.Protocol — not construct one.
//
// cmd/ and examples/ are intentionally outside the scan scope:
//   - cmd/* is the composition root by definition.
//   - examples/* each carry their own composition root; allowing them mirrors
//     the AUTH-PLAN-04 / LAYER-09 carve-out for example projects.
func CheckCASProtocolCompositionRoot01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(
		TypedOpts{Tests: false},
		casProtocolProductionPatterns(),
	), scanCASProtocolViolations)
}

// casProtocolProductionPatterns returns the package patterns scanned by the
// production rule (cells / runtime / adapters).
func casProtocolProductionPatterns() []string {
	return []string{
		"./corecells/...",
		"./runtime/...",
		"./adapters/...",
	}
}

// scanCASProtocolViolations walks every CallExpr in pass.Files, resolves the
// callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and flags
// hits whose owning package is runtime/state/cas and whose name is in
// casProtocolForbidden.
//
// Two file-level filters apply:
//
//   - _test.go suffix: defense-in-depth alongside TypedOpts{Tests: false}.
//   - rel under "runtime/state/cas/": the package itself owns the constructor.
//
// Used by both TestCASProtocol_CompositionRootOnly (production scan, asserts
// zero diagnostics) and TestCASProtocol_RedFixtureDetected (fixture scan,
// asserts exactly 3 diagnostics across qualified / aliased / dot-import shapes).
func scanCASProtocolViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		// Exempt the cas package itself and its sub-packages.
		if rel == casOwnPkgExact || strings.HasPrefix(rel, casOwnPkgPrefix) {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok || pkgPath != casPkgPath {
				return
			}
			if _, banned := casProtocolForbidden[name]; !banned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"cas.%s must only be called from cmd/* (composition root) or runtime/state/cas/*; "+
						"cells / runtime (non-cas) / adapters must consume an injected *cas.Protocol",
					name,
				),
			})
		})
	}
	return out
}
