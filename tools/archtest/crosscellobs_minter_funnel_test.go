//go:build archtest

// crosscellobs_minter_funnel_test.go — locks the SOLE sanctioned minter of the
// sealed transport.CrossCellObs cross-cell observability bundle (#2251 P1.3,
// epic #1423).
//
// INVARIANT: CROSSCELLOBS-MINTER-FUNNEL-01
//
// # What this guards
//
// transport.CrossCellObs bundles the metrics + tracer a remote CellTransport
// needs into ONE value, so that celltransport.Resolve receives a single pre-built
// param and the asymmetric "wired metrics but forgot the tracer" state is
// unrepresentable at that boundary (the Resolve sealed-param seal is Hard). The
// SECONDARY guarantee — the bundle's tracer is the SAME source composition.Builder
// threads into in-process spans via bootstrap.WithTracer, so remote and in-proc
// spans never diverge (ADR 202606131142-1423 D4) — depends on composition.Builder
// being the ONLY place that mints the bundle. transport.NewCrossCellObs is an
// exported constructor, so any package could mint its own bundle with a divergent
// tracer; the type system cannot express "only composition may mint" across module
// boundaries.
//
// This archtest pins every production reference of transport.NewCrossCellObs to
// the sole sanctioned minter, framework/runtime/composition (Builder.Build), which
// mints it from the single-source SharedDeps.Tracer + shared transport metrics.
// Modules receive the pre-built bundle via composition.SharedDeps.TransportObs;
// they never call the constructor themselves.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (sealed construction): HARD — CrossCellObs's fields are
//     unexported, so a bundle cannot be struct-literal-forged out-of-package; the
//     only mint path is the constructor.
//   - Upstream (only composition.Builder mints): MEDIUM — this caller-allowlist
//     archtest, a deliberately accepted permanent ceiling, not a deferred TODO.
//     CrossCellObs lives in framework/runtime/transport so celltransport (root
//     module) can consume its accessors; the minter is framework/runtime/
//     composition; the consumer is cellmodules/celltransport — three packages
//     across two modules with unexported fields owned by transport. A sealed mint
//     token from composition would be a cross-module import cycle, so Go visibility
//     cannot express "only composition may mint". Same Go/module ceiling documented
//     for EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01, the RowScopeAll minter,
//     COMMAND-ASYNC-EMIT-CALLER-01, OUTBOX-RECONSTRUCTION-CALLER-01
//     (#851/#893/#1282). No fake Hard-upgrade issue is opened.
//
// # Detection is type-aware (not string scanning)
//
// The scan visits every ast.SelectorExpr and resolves it via ResolvePackageRef to
// runtime/transport.NewCrossCellObs (alias- and dot-import-proof). It is a
// SelectorExpr-level (reference) scan, NOT a CallExpr.Fun-only scan, so it catches
// both a direct call (transport.NewCrossCellObs()) AND a function-value reference
// (mint := transport.NewCrossCellObs; mint()) — the latter would evade a
// CallExpr.Fun scan because the resulting call's Fun is a local ident.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. A bundle minted through a function value (mint := transport.NewCrossCellObs;
//     mint()) IS caught: the assignment's RHS is the SelectorExpr
//     transport.NewCrossCellObs, which the SelectorExpr-level scan resolves
//     directly (regression-tested by the fixture's ForgeViaFuncValue RED case).
//  2. _test.go files are out of scope (Production scope, Tests:false): test helpers
//     mint bundles to exercise the constructor; a bundle minted in a test never
//     reaches production wiring.
//  3. The RED fixture is build-tagged (archtest_fixture), excluded from the
//     default-tags Production scan, so it cannot pollute the production result.
//  4. Reflection-based construction is out of scope (theoretical, same posture as
//     EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01).
package archtest

import (
	"fmt"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

// crossCellObsMinterName is the sealed bundle minter on the transport package.
// runtimeTransportModule (the transport import path) is reused from
// celltransport_select_funnel.go (same archtest package).
const crossCellObsMinterName = "NewCrossCellObs"

// crossCellObsMinterPkgPath is the SOLE sanctioned caller package:
// framework/runtime/composition, whose Builder.Build mints the bundle from the
// single-source SharedDeps.Tracer + shared transport metrics.
const crossCellObsMinterPkgPath = PlatformFrameworkModulePath + "/runtime/composition"

// crossCellObsMintFixturePkg is the build-tagged RED fixture exercised by the
// reverse self-check.
const crossCellObsMintFixturePkg = "./tools/archtest/internal/crosscellobsmintfixture"

// isNewCrossCellObsRef reports whether sel references transport.NewCrossCellObs
// (alias/dot-import-proof via ResolvePackageRef). Resolving the SelectorExpr (not
// a CallExpr.Fun) catches BOTH a direct call (transport.NewCrossCellObs()) and a
// function-value reference (mint := transport.NewCrossCellObs; mint()).
func isNewCrossCellObsRef(p *Pass, sel *ast.SelectorExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
	return ok && pkgPath == runtimeTransportModule && name == crossCellObsMinterName
}

// TestCrossCellObsMinterFunnel01 asserts every production reference of
// transport.NewCrossCellObs sits in framework/runtime/composition, and that
// composition actually mints it (anti-vacuity).
func TestCrossCellObsMinterFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var observed, observedInFunnel bool
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		inFunnel := p.Pkg.Path() == crossCellObsMinterPkgPath
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isNewCrossCellObsRef(p, sel) {
					return
				}
				observed = true
				if inFunnel {
					observedInFunnel = true
					return // composition.Builder is the sanctioned minter
				}
				pos := p.Fset.Position(sel.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"CROSSCELLOBS-MINTER-FUNNEL-01: %s references transport.NewCrossCellObs "+
							"(direct call OR function-value reference) outside the sole sanctioned minter "+
							"framework/runtime/composition.Builder. The CrossCellObs bundle binds the remote "+
							"transport's tracer to the SAME source composition threads into in-process spans via "+
							"bootstrap.WithTracer; minting it elsewhere can pair a divergent tracer with remote "+
							"calls, breaking the single-source span invariant (ADR 202606131142-1423 D4). Receive "+
							"the pre-built bundle from composition.SharedDeps.TransportObs instead; or, if this is "+
							"a genuinely new sanctioned minter, widen the funnel allowlist with a rationale.",
						rel),
				})
			})
		}
		return d
	})

	// Anti-vacuity: composition must actually mint the bundle, else the funnel
	// guards nothing (the minter was removed/renamed or the scanner regressed).
	if !observed {
		diags = append(diags, Diagnostic{
			Message: "CROSSCELLOBS-MINTER-FUNNEL-01 anti-vacuity: no production reference to " +
				"transport.NewCrossCellObs() was observed anywhere. composition.Builder.Build must mint it — " +
				"either the minter was removed/renamed or the scanner regressed; the funnel guards nothing.",
		})
	}
	// Second anti-vacuity: the observed mint must occur INSIDE the sanctioned
	// minter package, exercising the allowlist GREEN path. If observed==true but
	// observedInFunnel==false, the only mint is somewhere else and the funnel's
	// suppression branch was never taken (the composition pkgPath drifted or the
	// minter moved out) — a silent regression of the funnel's anchor.
	if observed && !observedInFunnel {
		diags = append(diags, Diagnostic{
			Message: "CROSSCELLOBS-MINTER-FUNNEL-01 anti-vacuity: transport.NewCrossCellObs() is minted in " +
				"production but NOT inside " + crossCellObsMinterPkgPath + "; the sanctioned-minter (GREEN) " +
				"path was never exercised. Either the minter moved out of composition.Builder or " +
				"crossCellObsMinterPkgPath drifted.",
		})
	}

	Report(t, "CROSSCELLOBS-MINTER-FUNNEL-01", diags)
}

// TestCrossCellObsMinterFunnel01_RedFixture verifies the scanner fires against a
// package that references transport.NewCrossCellObs outside composition — BOTH as
// a direct call (ForgeBundle) AND as a function-value reference (ForgeViaFuncValue)
// — and does NOT flag the SafeOtherCtor (NewInProcess) GREEN control. found==0
// means the scanner is fail-open (anti-vacuity for the detector itself); found==1
// would mean the function-value reference still evades.
func TestCrossCellObsMinterFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{crossCellObsMintFixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// The fixture package path is not framework/runtime/composition, so any
		// NewCrossCellObs reference there is a violation; NewInProcess (the GREEN
		// control) must not be counted.
		for _, file := range p.Files {
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if isNewCrossCellObsRef(p, sel) {
					found++
				}
			})
		}
		return nil
	})

	assert.Equal(t, 2, found,
		"CROSSCELLOBS-MINTER-FUNNEL-01 RED fixture self-check FAILED: expected exactly 2 violations from "+
			"crosscellobsmintfixture (ForgeBundle's direct call + ForgeViaFuncValue's function-value reference; "+
			"the SafeOtherCtor NewInProcess control must NOT be flagged); got %d. found==1 means a function-value "+
			"reference still evades the scan; found==0 means the scanner is fail-open — check ResolvePackageRef "+
			"resolves SelectorExpr under the archtest_fixture tag.", found)
}
