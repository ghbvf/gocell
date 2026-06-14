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
// A Codex review (PR #2092 F1) flagged that an exported concrete with a usable
// zero value could be Bound outside the composition root. The bind authority is
// now closed at TWO layers:
//
//  1. Type layer (Hard, in runtime/transport): the BINDABLE concrete is the
//     UNEXPORTED inProcessDispatcher (unexpressible outside the package). The
//     exported InProcessTransport is a thin handle whose only field is that
//     unexported dispatcher, so a forged zero-value InProcessTransport{} has a
//     nil dispatcher and its Bind / DoContract fail-fast (inert). Thus the ONLY
//     way to obtain a usable, bindable handle is NewInProcess.
//  2. Caller layer (this rule, Medium): NewInProcess — the sole producer of a
//     usable handle — may only be called from the composition root. Consumer
//     cells receive the CellTransport INTERFACE (DoContract only, no Bind) via
//     configgetter; bootstrap receives the already-minted handle via the typed
//     WithInProcessTransport option and is the sole legitimate Bind caller.
//
// So minting authority == bind authority, and locking the minter to composition
// (this rule) on top of the unforgeable bindable concrete (the type layer) closes
// both — a zero-value forge is inert AND an out-of-composition NewInProcess is red.
//
// ## AI-robust rating: Medium (this rule) — backed by a Hard type layer
//
// This rule is a caller-allowlist constraint ("only composition may call
// NewInProcess"), same carrier class as REPLAYDEPS-INMEM-FUNNEL-01 /
// SVCTOKEN-CALLER-CELL-REQUIRED-01: a typed callsite scan via ResolvePackageRef is
// the reachable ceiling. The HARD half lives in the type system: the bindable
// dispatcher is unexported (cannot be constructed externally) and the zero-value
// handle fail-fasts (TestInProcessTransport_ZeroValue_FailsFast). Together with
// INPROCESS-TRANSPORT-SEALED-01 (Hard field freeze) they form the closed
// bind-authority funnel.
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
const transportPkgPath = PlatformFrameworkModulePath + "/runtime/transport"

// newInProcessAllowedCallers is the closed set of packages that may mint an
// InProcessTransport. Only the composition root (Builder.Build) mints the single
// shared holder; bootstrap receives it via WithInProcessTransport (does not mint).
var newInProcessAllowedCallers = map[string]struct{}{
	PlatformFrameworkModulePath + "/runtime/composition": {},
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
		{transportPkgPath, "ModeInProc"},                                       // not the minter
		{transportPkgPath, "CellTransport"},                                    // the interface
		{transportPkgPath, "NewMetrics"},                                       // a sibling constructor
		{PlatformFrameworkModulePath + "/runtime/composition", "NewInProcess"}, // wrong pkg (a cell's own NewInProcess)
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
