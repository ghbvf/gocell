//go:build archtest

// INVARIANT: PROJECTION-CONSUMERBASE-WIRING-01
//
// PROJECTION-CONSUMERBASE-WIRING-01 — composition-root projection↔ConsumerBase
// wiring co-location.
//
// Background (#1497 / PR #1483): bootstrap phase6 (the projection coordinator
// stage) consumes projections through the same ConsumerBase path as event
// subscriptions, so a composition root that registers projections MUST wire
// bootstrap.WithConsumerBase — otherwise Run fails fast at startup. PR #1483
// shipped exactly that bug: examples/todoorder/run.go wired the projection infra
// options (WithProjectionCheckpointStore / ReplaySource / Cursor) but omitted
// WithConsumerBase, so `go run ./examples/todoorder` crashed at phase6 while CI
// stayed green (no test booted the example, and no static rule caught it).
//
// This rule lifts that phase6 runtime invariant to a CI-static guard, exactly as
// the issue requested ("断言 projection wiring 完整（含 ConsumerBase），无需真实
// env/listener"): any composition-root package (examples/* or cmd/*) that calls
// any bootstrap.WithProjection* option MUST also call bootstrap.WithConsumerBase
// somewhere in the SAME package. It auto-covers todoorder + cmd/corebundle today
// and every future composition root, with no boot, ports, or env. The
// defense-in-depth runtime counterpart is the todoorder startup smoke
// (examples/todoorder/run_smoke_test.go), which catches any boot-breaking wiring
// drift (not just the ConsumerBase class).
//
// # AI-robust grading (Funnel 双向锁评级)
//
//   - Downstream: Hard — ResolvePackageRef binds each callee's (pkgPath, name)
//     via go/types, so alias imports and dot-imports cannot bypass it (the form
//     is type-resolved, not string/AST-matched).
//   - Upstream: Medium — this is a co-location presence check. Go cannot make
//     "wire WithProjection* without WithConsumerBase" unrepresentable; the
//     archtest is the strongest achievable form. The only Hard upstream path is
//     a kernel redesign threading a ConsumerBase typed token into projection
//     registration so phase6 is unreachable without one — a high-cost change far
//     beyond this invariant's value, won't-do (tracked at gh #1597, same
//     Go-language-ceiling family as #851 / #893 / #1282). This archtest is the
//     intended permanent form; the runtime smoke is the complementary backstop.
//
// # Blind spots
//
//   - B1. Function-value indirection (`opt := bootstrap.WithProjectionCursor; opt(c)`):
//     the CallExpr Fun is an *ast.Ident → *types.Var, invisible to
//     ResolvePackageRef, so a projection option taken as a value (never directly
//     called) escapes detection (fail-open). Covered by
//     TestProjectionConsumerBaseWiring_ReverseBlindSpot_NoFuncValue, which
//     asserts no composition-root production file holds such a function value.
//   - B2. Cross-package split: WithProjection* in package A and WithConsumerBase
//     in package B would false-positive A. The granularity is package-level by
//     design; today both options always co-locate in the single composition-root
//     package (examples/todoorder = package main; cmd/corebundle = one package).
//     A composition root spanning multiple packages is out of scope and would
//     need an explicit allowlist if it ever arises.
//   - B3 (not a blind spot): dot-import of runtime/bootstrap is resolved by
//     ResolvePackageRef (go/types object identity), so `WithProjectionCursor(...)`
//     after `import . ".../runtime/bootstrap"` is matched normally.
//
// ref: tools/archtest/projection_register_funnel_test.go (sibling projection funnel)
// ref: examples/todoorder/run_smoke_test.go (runtime defense-in-depth counterpart)
package archtest

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// projectionWiringBootstrapPkgPath is the package owning the WithProjection* /
// WithConsumerBase bootstrap options. Derived from PlatformModulePath
// (ARCHTEST-MODULE-PATH-FUNNEL-01: no bare module-path literal).
const projectionWiringBootstrapPkgPath = PlatformFrameworkModulePath + "/runtime/bootstrap"

// projectionWiringConsumerBaseOption is the option whose presence the rule
// requires whenever any projectionWiringOptions entry is wired.
const projectionWiringConsumerBaseOption = "WithConsumerBase"

// projectionWiringOptions is the set of bootstrap projection options whose
// presence implies the OUTBOX projection coordinator runs at phase6 — which
// consumes via ConsumerBase. Any one of them in a composition-root package
// triggers the WithConsumerBase requirement.
//
// WithProjectionTxRunner is deliberately EXCLUDED: it is shared infra reused by
// BOTH the outbox Coordinator AND the saga-journal Tailer path (options_saga_
// projection.go: "The TxRunner is REUSED from WithProjectionTxRunner"). A
// saga-journal-only composition root (e.g. examples/orderfulfillment) wires
// WithProjectionTxRunner + WithSagaJournalReader/SagaProjection* but no outbox
// coordinator and legitimately needs no ConsumerBase — phase6 drains its Tailer
// independently (cellSnapshotsHaveProjections excludes ProjectionSourceSagaJournal)
// and short-circuits the router/ConsumerBase check, so it boots fine. The
// outbox-coordinator-exclusive options below are the true trigger: a real outbox
// projection root always wires CheckpointStore + ReplaySource + Cursor (the
// coordinator's required deps; TxRunner alone cannot reach it), so excluding
// TxRunner keeps todoorder + cmd/corebundle + the reverse fixture covered while
// dropping the saga-journal false positive. See TestProjectionConsumerBaseWiring_
// SagaJournalOnly_NoFire for the regression lock.
//
// Note: a saga-journal root that ALSO wires an outbox-coordinator option (e.g.
// WithProjectionCheckpointStore) still fires — correctly, since it then runs the
// outbox coordinator too and must wire WithConsumerBase.
//
// ref: framework/runtime/bootstrap/phases_projection.go — cellSnapshotsHave
// Projections excludes ProjectionSourceSagaJournal; that phase6 gating is the
// source of truth this exclusion tracks, so re-verify here if it changes.
var projectionWiringOptions = map[string]bool{
	"WithProjectionCheckpointStore": true,
	"WithProjectionReplaySource":    true,
	"WithProjectionCursor":          true,
	"WithProjectionRebuildEndpoint": true,
}

// isProjectionCompositionRootPkg reports whether pkgPath is a composition root
// that may wire bootstrap options — examples/* or cmd/*. Cells/runtime/kernel do
// not assemble bootstrap option slices, so they are out of scope.
//
// cmd/* is included conservatively (covers cmd/corebundle, which DOES wire
// projections). It also matches the cmd/gocell governance CLI, which never wires
// bootstrap today and is therefore a vacuous pass; if it ever wires
// WithProjection* it will (correctly) be required to co-locate WithConsumerBase.
func isProjectionCompositionRootPkg(pkgPath string) bool {
	return strings.HasPrefix(pkgPath, PlatformModulePath+"/examples/") ||
		strings.HasPrefix(pkgPath, PlatformModulePath+"/cmd/")
}

// scanPkgProjectionWiring scans every file in one package for direct calls to
// bootstrap projection options and WithConsumerBase, resolving callees via
// go/types. It returns whether each family is present plus the rel/line of the
// first projection option (for diagnostic anchoring). It applies NO scope filter
// so it can be reused over the standalone reverse fixture.
func scanPkgProjectionWiring(p *Pass) (hasProjection, hasConsumerBase bool, projRel string, projLine int) {
	for _, f := range p.Files {
		rel := p.Rel(f)
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			fun := call.Fun
			if idx, ok := fun.(*ast.IndexExpr); ok {
				fun = idx.X
			} else if idxl, ok := fun.(*ast.IndexListExpr); ok {
				fun = idxl.X
			}
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, fun)
			if !ok || pkgPath != projectionWiringBootstrapPkgPath {
				return
			}
			switch {
			case name == projectionWiringConsumerBaseOption:
				hasConsumerBase = true
			case projectionWiringOptions[name]:
				if !hasProjection {
					hasProjection = true
					projRel = rel
					projLine = p.Fset.Position(call.Pos()).Line
				}
			}
		})
	}
	return hasProjection, hasConsumerBase, projRel, projLine
}

// TestProjectionConsumerBaseWiring enforces PROJECTION-CONSUMERBASE-WIRING-01:
// every composition-root package (examples/* or cmd/*) that wires any
// bootstrap.WithProjection* option must also wire bootstrap.WithConsumerBase.
func TestProjectionConsumerBaseWiring(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if !isProjectionCompositionRootPkg(p.Pkg.Path()) {
			return nil
		}
		hasProjection, hasConsumerBase, projRel, projLine := scanPkgProjectionWiring(p)
		if hasProjection && !hasConsumerBase {
			return []Diagnostic{{
				Rel:  projRel,
				Line: projLine,
				Message: fmt.Sprintf(
					"PROJECTION-CONSUMERBASE-WIRING-01: composition-root package %q wires "+
						"bootstrap.WithProjection* but not bootstrap.WithConsumerBase. Projections "+
						"consume via the ConsumerBase path and bootstrap phase6 fails fast at startup "+
						"without it (PR #1483 regression). Add bootstrap.WithConsumerBase(...) to this "+
						"package's bootstrap options.",
					p.Pkg.Path(),
				),
			}}
		}
		return nil
	})

	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Rel != diags[j].Rel {
			return diags[i].Rel < diags[j].Rel
		}
		return diags[i].Line < diags[j].Line
	})
	Report(t, "PROJECTION-CONSUMERBASE-WIRING-01", diags)
}

// TestProjectionConsumerBaseWiring_ReverseFixture loads the synthetic violation
// fixture (a package that wires bootstrap.WithProjection* without
// WithConsumerBase) and asserts the rule fires — proving it is not fail-open.
func TestProjectionConsumerBaseWiring_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "projection_consumerbase_violate")

	var fired bool
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			hasProjection, hasConsumerBase, _, _ := scanPkgProjectionWiring(p)
			if hasProjection && !hasConsumerBase {
				fired = true
			}
			return nil
		})

	assert.True(t, fired,
		"fixture wiring bootstrap.WithProjection* without WithConsumerBase MUST fire "+
			"PROJECTION-CONSUMERBASE-WIRING-01 (rule must not be fail-open)")
}

// TestProjectionConsumerBaseWiring_SagaJournalOnly_NoFire is the GREEN regression
// lock for the WithProjectionTxRunner false positive: a saga-journal-only
// composition root (the examples/orderfulfillment shape) wires the shared
// bootstrap.WithProjectionTxRunner but no outbox-coordinator option and no
// WithConsumerBase. Such a root boots fine — phase6 drains its saga-journal
// Tailer independently of the ConsumerBase router — so the rule MUST NOT fire.
// If a future edit re-adds WithProjectionTxRunner to projectionWiringOptions,
// this turns red (locking the fix against silent regression).
func TestProjectionConsumerBaseWiring_SagaJournalOnly_NoFire(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "projection_consumerbase_sagajournal_ok")

	var fired bool
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			hasProjection, hasConsumerBase, _, _ := scanPkgProjectionWiring(p)
			if hasProjection && !hasConsumerBase {
				fired = true
			}
			return nil
		})

	assert.False(t, fired,
		"saga-journal-only root wiring bootstrap.WithProjectionTxRunner (shared infra) "+
			"without WithConsumerBase MUST NOT fire PROJECTION-CONSUMERBASE-WIRING-01 "+
			"(it boots fine; phase6 drains the Tailer independently)")
}

// TestProjectionConsumerBaseWiring_ReverseBlindSpot_NoFuncValue (blind spot B1)
// asserts no composition-root production file holds a bootstrap projection option
// (or WithConsumerBase) as a function value rather than a direct call — such an
// indirection would escape ResolvePackageRef on call.Fun and fail the rule open.
func TestProjectionConsumerBaseWiring_ReverseBlindSpot_NoFuncValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	var violations []string

	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if !isProjectionCompositionRootPkg(p.Pkg.Path()) {
			return nil
		}
		// Collect the inner Sel ident of every SelectorExpr that IS a direct
		// callee (call.Fun) so a direct option call is not mistaken for a value.
		// Keyed by the per-occurrence *ast.Ident node (each occurrence is a
		// distinct AST node), matching projection_register_funnel_test.go's B1.
		calleeSel := make(map[*ast.Ident]struct{})
		for _, f := range p.Files {
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					calleeSel[sel.Sel] = struct{}{}
				}
			})
		}

		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
				if _, isCallee := calleeSel[sel.Sel]; isCallee {
					return
				}
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
				if !ok || pkgPath != projectionWiringBootstrapPkgPath {
					return
				}
				if name != projectionWiringConsumerBaseOption && !projectionWiringOptions[name] {
					return
				}
				violations = append(violations, fmt.Sprintf(
					"%s:%d: bootstrap.%s used as a function value (not a direct call) "+
						"— would escape PROJECTION-CONSUMERBASE-WIRING-01 detection (blind spot B1)",
					rel, p.Fset.Position(sel.Pos()).Line, name))
			})
		}
		return nil
	})

	assert.Empty(t, violations,
		"blind spot B1: no composition-root production file may hold a bootstrap "+
			"projection/ConsumerBase option as a function value")
}
