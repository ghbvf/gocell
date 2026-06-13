//go:build archtest

// INVARIANT: INPROCESS-TRANSPORT-BIND-AUTHORITY-01
//
// # INPROCESS-TRANSPORT-BIND-AUTHORITY-01 — only the composition root may mint an InProcessTransport (Medium)
//
// ## Rule
//
// `transport.NewInProcess` (the sole constructor of the bindable in-process
// transport) may be called ONLY from the composition root (`runtime/composition`,
// where Builder.Build mints the single shared holder). No other production
// package may call it.
//
// ## Why (the bind-authority closure)
//
// A Codex review (PR #2092 F1) flagged that `NewInProcess` + `Bind` are exported,
// so any package could construct an InProcessTransport and Bind an arbitrary
// handler — escaping the "composition-root-only" bind authority. This rule closes
// the CONSTRUCTION chokepoint: restricting `NewInProcess` to runtime/composition
// transitively gates `Bind`, because the bindable concrete `*InProcessTransport`
// can only be obtained from `NewInProcess`. Consumer cells receive the
// `transport.CellTransport` INTERFACE (only `DoContract`, no `Bind`) via
// configgetter, so they cannot bind; bootstrap receives the already-minted holder
// via the typed `WithInProcessTransport` option and is the sole legitimate
// `Bind` caller. Thus minting authority == bind authority, and locking the minter
// to composition closes both.
//
// ## AI-robust rating: Medium — and why Hard is not pursued here
//
// This is a caller-allowlist constraint ("only composition may call
// NewInProcess"), the same carrier class as REPLAYDEPS-INMEM-FUNNEL-01 and
// SVCTOKEN-CALLER-CELL-REQUIRED-01: a typed callsite scan via ResolvePackageRef is
// the reachable ceiling. A Hard form (e.g. a sealed bind-capability token only
// the composition root mints) is high-cost for a single minter already gated by
// the interface boundary — no low-cost Hard path (ai-robust.md §审查要求).
// The upstream sealed type (INPROCESS-TRANSPORT-SEALED-01, Hard) plus this Medium
// caller funnel together form the closed bind-authority funnel.
//
// ## Blind spots / anti-vacuity
//
//   - Vacuity: only runtime/composition calls NewInProcess in production, so the
//     production scan (TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01) is vacuously
//     green. Anti-vacuity: the synthetic detector table + the RED fixture
//     (synctransportfixture.forgeTransport calls NewInProcess outside composition).
//   - cmd/corebundle's e2e test constructs NewInProcess — a _test.go, excluded by
//     the Tests:false production scan (tests legitimately build transports).
//   - Dot-import: bare `NewInProcess(...)` not walked (selector-only); cells
//     cannot dot-import (revive linter is the gate).
//
// ref: tools/archtest/replaydeps_inmem_funnel.go (caller-allowlist funnel template)
// ref: tools/archtest/cell_sync_transport_funnel_test.go (sibling US4 funnel)
package archtest

import (
	"fmt"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleInProcessBindAuthority = "INPROCESS-TRANSPORT-BIND-AUTHORITY-01"

// transportPkgPath is the import path owning NewInProcess + InProcessTransport.
const transportPkgPath = PlatformModulePath + "/runtime/transport"

// newInProcessAllowedCallers is the closed set of packages that may mint an
// InProcessTransport. Only the composition root (Builder.Build) mints the single
// shared holder; bootstrap receives it via WithInProcessTransport (does not mint).
var newInProcessAllowedCallers = map[string]struct{}{
	PlatformModulePath + "/runtime/composition": {},
}

// isNewInProcessRef reports whether (pkgPath, name) refers to
// transport.NewInProcess — the bindable-transport minter.
func isNewInProcessRef(pkgPath, name string) bool {
	return pkgPath == transportPkgPath && name == "NewInProcess"
}

// scanNewInProcessCallers emits a Diagnostic for every transport.NewInProcess
// reference in a file. The caller decides whether the owning package is
// allowlisted (production scan) or a fixture (fixture scan).
func scanNewInProcessCallers(p *Pass, file *ast.File, rel string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		path, name, ok := ResolvePackageRef(p.TypesInfo, sel)
		if !ok || !isNewInProcessRef(path, name) {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s: %s references transport.NewInProcess at %s:%d — only the composition root "+
					"(runtime/composition) may mint the in-process transport; an external minter could Bind a "+
					"rogue handler, escaping composition-root bind authority.",
				ruleInProcessBindAuthority, rel, rel, line),
		})
	})
	return diags
}

// TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01 is the production scan: no production
// package outside the allowlist may call transport.NewInProcess. Vacuously green
// today (only runtime/composition mints) — see the godoc Vacuity note.
func TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if _, ok := newInProcessAllowedCallers[p.Pkg.Path()]; ok {
				return nil // composition root is the sanctioned minter
			}
			if p.Pkg.Path() == transportPkgPath {
				return nil // the package defining NewInProcess (the constructor body itself)
			}
			for _, f := range p.Files {
				diags = append(diags, scanNewInProcessCallers(p, f, p.Rel(f))...)
			}
			return nil
		})

	Report(t, ruleInProcessBindAuthority, diags)
}

// TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01_SyntheticDetector pins the pure
// isNewInProcessRef classifier, proving the detector branch is reachable
// independent of the repo's vacuous production state.
func TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01_SyntheticDetector(t *testing.T) {
	t.Parallel()

	assert.True(t, isNewInProcessRef(transportPkgPath, "NewInProcess"), "the minter must be detected")

	green := []struct{ pkg, name string }{
		{transportPkgPath, "ModeInProc"},      // not the minter
		{transportPkgPath, "CellTransport"},   // the interface
		{transportPkgPath, "NewMetrics"},      // a sibling constructor
		{PlatformModulePath + "/runtime/composition", "NewInProcess"}, // wrong pkg (a cell's own NewInProcess)
	}
	for _, tc := range green {
		assert.Falsef(t, isNewInProcessRef(tc.pkg, tc.name), "%s.%s must NOT be detected", tc.pkg, tc.name)
	}
}

// TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01_FixtureScanRED runs the real scanner
// over the RED fixture (which mints a transport outside composition) and asserts
// it fires — proving the scan layer is load-bearing (anti-vacuity).
func TestINPROCESS_TRANSPORT_BIND_AUTHORITY_01_FixtureScanRED(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{syncTransportFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				d = append(d, scanNewInProcessCallers(p, f, p.Rel(f))...)
			}
			return d
		})

	assert.NotEmpty(t, diags,
		"INPROCESS-TRANSPORT-BIND-AUTHORITY-01 RED fixture must fire (anti-vacuity); a miss means the "+
			"resolver or the NewInProcess detector regressed")
	for _, d := range diags {
		assert.Contains(t, d.Rel, "synctransportfixture", "diagnostic must point at the fixture")
		assert.Contains(t, d.Message, ruleInProcessBindAuthority, "diagnostic must carry the rule ID")
	}
}
