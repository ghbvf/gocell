// INVARIANT: CELL-L2-INIT-CHECKNOTNOOP-CALLED-01
//
// # CELL-L2-INIT-CHECKNOTNOOP-CALLED-01
//
// Every cell whose cell.yaml declares consistencyLevel >= "L2" MUST have its
// `(*GoStructName).Init` method body — or a transitive callee defined in the
// same Go package — invoke `kernel/cell.CheckNotNoop` at least once. Missing
// the call means a future durable-mode wiring mistake (noop publisher / noop
// writer / DemoCellTxManager left in place) will pass through Init silently;
// CheckNotNoop is the runtime guard that turns such mistakes into Init-time
// errors instead of silent durability degradation.
//
// This archtest is the **static defense layer** complementing OUTGUARD-01
// (governance rule) which guards the cell.yaml metadata layer: OUTGUARD-01
// requires L2+ cells to declare `durabilityMode`, this archtest requires the
// implementation to wire the runtime guard.
//
// The archtest does NOT solve the upstream nil-writer divergence reported by
// finding CONFIGCORE-L2-MEMORY-MODE-DIVERGENCE-01 — that body is deferred to
// backlog BASECELL-DURABILITYMODE-RUNTIME-ALIGNMENT-DEFERRED. This archtest
// only protects against future L2+ cells forgetting the CheckNotNoop call.
//
// AI-rebust grade: Medium (type-aware AST funnel via RunTypedProduction +
// *types.Info callee resolution; scoped to production packages). Hard
// upgrade candidates are tracked in plan
// `docs/plans/202605101548-035-configcore-residuals-fix-plan.md` §Tradeoff
// (codegen funnel / CheckNotNoop nil-reject / drop memory mode).
//
// # Algorithm
//
// Phase A — enumerate targets:
//
//	EachContentFile over cells/**/cell.yaml; yaml.Unmarshal into a minimal
//	struct {ID, ConsistencyLevel, GoStructName}; keep those with
//	ConsistencyLevel >= "L2" and a non-empty GoStructName.
//
// Phase B — scan production code per target:
//
//	RunTypedProduction loads the production package set. For each target
//	cell, find its package by import path suffix (`/cells/<cellID>`),
//	locate the Init FuncDecl on `*GoStructName`, then BFS over same-package
//	callees of Init. A target satisfies the rule if any visited CallExpr
//	resolves via *types.Info to `kernel/cell.CheckNotNoop`. Diagnostics
//	reference the cell.yaml path and the Init receiver position.
//
// # Blind-spot inventory
//
// The Medium grade is bounded by the following AST shapes that the Phase B
// scanner does NOT detect; each is paired with a reverse-self-check test in
// this file asserting the shape does not appear in current production AST:
//
//  1. `reflect.ValueOf(cell.CheckNotNoop).Call(...)`. Reflective dispatch
//     hides the callee from *types.Info. Reverse check:
//     TestNoReflectCheckNotNoopInProduction.
//
//  2. `//go:linkname` aliasing `kernel/cell.CheckNotNoop` under another name.
//     Reverse check: TestNoLinknameAliasInProduction.
//
//  3. `go func() { cell.CheckNotNoop(...) }()` — async dispatch from the
//     Init reachable set. The call is statically reachable but not part of
//     the synchronous Init contract (runtime may not have executed before
//     Init returns). Reverse check: TestNoAsyncCheckNotNoopInProduction.
//
//  4. Cross-package indirection (e.g. an `internal/wrapcheck.Check(...)`
//     helper in another package that internally calls CheckNotNoop). Phase B
//     restricts BFS to same-package callees by design — Init/initInternal
//     direct call is the K#04 hand-written hook convention; cross-package
//     indirection is rejected as a Soft-ification escape. The
//     red_cross_pkg fixture pins this contract.
//
// ref: docs/plans/202605101548-035-configcore-residuals-fix-plan.md
// ref: docs/backlog/cap-14-tooling.md BASECELL-DURABILITYMODE-RUNTIME-ALIGNMENT-DEFERRED
//
//	(Hard upgrade candidates: codegen funnel / CheckNotNoop nil-reject /
//	drop memory mode — coupled to that deferred body)
//
// ref: kernel/cell/durability.go (CheckNotNoop, Nooper)
// ref: AI-rebust §载体决策原则 in .claude/rules/gocell/ai-collab.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// cellInitCheckNotNoopRuleID is the diagnostic anchor for this archtest.
const cellInitCheckNotNoopRuleID = "CELL-L2-INIT-CHECKNOTNOOP-CALLED-01"

// kernelCellCheckNotNoopFullName is the fully-qualified callee name produced
// by `(*types.Func).FullName()` for `kernel/cell.CheckNotNoop`. Used as the
// equality probe in Phase B's BFS — string match on FullName is rename-safe
// across packages and import aliases (handled by *types.Info.Uses lookup).
const kernelCellCheckNotNoopFullName = "github.com/ghbvf/gocell/kernel/cell.CheckNotNoop"

// l2TargetCell captures the minimal data Phase A collects from cell.yaml to
// drive Phase B's production scan.
type l2TargetCell struct {
	cellID       string // metadata ID
	goStructName string // CamelCase Go type, declared in cell.yaml.goStructName
	yamlPath     string // path/to/cells/<id>/cell.yaml (project-relative)
	pkgSuffix    string // import-path suffix used to match Pass.Pkg.Path()
}

// cellYAMLSubset is the minimal YAML projection consumed by Phase A. Reading
// only the fields we need decouples the archtest from incidental schema
// growth in kernel/metadata.CellMeta.
type cellYAMLSubset struct {
	ID               string `yaml:"id"`
	ConsistencyLevel string `yaml:"consistencyLevel"`
	GoStructName     string `yaml:"goStructName"`
}

// consistencyLevelAtLeastL2 reports whether the YAML-declared level meets the
// L2+ threshold. "L0"-"L4" are the canonical values and lexicographically
// order identically to their semantic ordering — string compare suffices and
// avoids a parallel ordinal helper, matching the encoding used by
// kernel/metadata.CellMeta.ConsistencyLevel.
//
// Strings outside the closed set {"L0","L1","L2","L3","L4"} (typos like
// "l2", future extensions like "L10", or empty omitted level) do NOT meet
// the threshold — the upper bound `<= "L4"` rejects them. OUTGUARD-01
// (kernel/governance/rules_misc_advisory.go) is the upstream gate that
// enforces metadata schema validity, so any non-canonical value reaching
// this helper has already been flagged by `gocell validate --strict`. The
// helper's defensive upper bound is a belt-and-suspenders against rule-
// ordering surprises; TestConsistencyLevelAtLeastL2 pins the boundary
// behavior with explicit cases for "L1"/"L2"/"L4"/"l2"/"L10"/"".
func consistencyLevelAtLeastL2(level string) bool {
	return level >= "L2" && level <= "L4"
}

// TestConsistencyLevelAtLeastL2 pins the closed-set boundary behavior of
// consistencyLevelAtLeastL2. The function relies on string compare against
// the closed canonical set; any future deviation (e.g. introducing "L10",
// non-canonical case, omitted level) is locked here as a unit-level
// counterexample so changes to the helper are observed without round-
// tripping through Phase A end-to-end fixtures.
func TestConsistencyLevelAtLeastL2(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level string
		want  bool
	}{
		{"L0", false},
		{"L1", false},
		{"L2", true},
		{"L3", true},
		{"L4", true},
		{"", false},    // omitted level
		{"l2", false},  // wrong case — must not slip through
		{"L10", false}, // hypothetical future extension — rejected by upper bound
		{"L5", false},  // out-of-band — rejected by upper bound
	}
	for _, tc := range cases {
		if got := consistencyLevelAtLeastL2(tc.level); got != tc.want {
			t.Errorf("consistencyLevelAtLeastL2(%q) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestCELL_L2_INIT_CHECKNOTNOOP_CALLED_01 is the end-to-end production check:
// every L2+ cell in cells/** must have its Init reach CheckNotNoop via same-
// package callees. Current platform cells (accesscore, auditcore, configcore)
// already satisfy this; the test exists to guard against future regressions.
func TestCELL_L2_INIT_CHECKNOTNOOP_CALLED_01(t *testing.T) {
	t.Parallel()

	scope := ModuleScope(findModuleRoot(t))
	targets := collectL2PlusTargets(t, scope)
	require.NotEmpty(t, targets,
		"expected at least one L2+ cell in cells/** — has the platform cell layout changed?")

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		return scanCellsForInitCheckNotNoop(p, targets)
	})

	Report(t, cellInitCheckNotNoopRuleID, diags)
}

// collectL2PlusTargets walks cell.yaml files under scope, parses the minimal
// subset, and returns one l2TargetCell per L2+ cell with a non-empty
// GoStructName. Cells without GoStructName are skipped (they do not opt into
// K#04 codegen Init, so the rule does not apply to them).
//
// Scope filtering: only cell.yaml files whose module-relative path starts
// with "cells/" are considered. `examples/<name>/cells/<id>/cell.yaml`
// (CLAUDE.md allows examples to ship their own cells) does NOT start with
// "cells/" and is intentionally skipped — the rule guards the platform
// cell layout only; example cells are demonstration scaffolding outside
// the deployed platform surface.
func collectL2PlusTargets(t *testing.T, scope Scope) []l2TargetCell {
	t.Helper()

	var targets []l2TargetCell
	EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc ContentContext) {
		// EachContentFile requires a dot-prefixed suffix; filter by basename
		// to recover cell.yaml-only scope. cells/X/cell.yaml is the only shape
		// we care about; ignore examples/* / contracts/* / slice.yaml etc.
		rel := filepath.ToSlash(fc.Rel)
		if filepath.Base(rel) != "cell.yaml" {
			return
		}
		if !strings.HasPrefix(rel, "cells/") {
			return
		}
		var meta cellYAMLSubset
		if err := yaml.Unmarshal(fc.Bytes, &meta); err != nil {
			t.Fatalf("cell.yaml parse %s: %v", fc.Rel, err)
		}
		if !consistencyLevelAtLeastL2(meta.ConsistencyLevel) {
			return
		}
		if meta.GoStructName == "" {
			// Non-codegen cells (no K#04 hook) — rule does not apply.
			return
		}
		cellDir := filepath.Dir(rel) // "cells/<id>"
		targets = append(targets, l2TargetCell{
			cellID:       meta.ID,
			goStructName: meta.GoStructName,
			yamlPath:     rel,
			pkgSuffix:    "/" + cellDir,
		})
	})
	return targets
}

// scanCellsForInitCheckNotNoop runs Phase B on a single Pass. It selects the
// l2TargetCell whose pkgSuffix matches Pass.Pkg.Path() (at most one match),
// locates the Init FuncDecl on the receiver `*GoStructName`, and runs a same-
// package BFS to find a CheckNotNoop callee. A diagnostic is emitted iff the
// BFS does not find one (or the Init method is missing).
//
// Diagnostics reference the target's yamlPath at line 1 so the cell.yaml
// declaration site is the navigation anchor; when an Init method is located
// but the BFS misses CheckNotNoop, the diagnostic message also embeds the
// Init's Go file path and line so developers can jump straight to the
// implementation site without grepping for the receiver type. The Init
// position is appended to Message rather than the Rel/Line pair because
// archtest Diagnostic carries a single position and the cell.yaml anchor
// remains the canonical declaration site.
func scanCellsForInitCheckNotNoop(p *Pass, targets []l2TargetCell) []Diagnostic {
	if p == nil || p.Pkg == nil {
		return nil
	}
	pkgPath := p.Pkg.Path()
	target := matchTarget(pkgPath, targets)
	if target == nil {
		return nil
	}
	initFn := initFuncDecl(p, target.goStructName)
	if initFn == nil {
		return []Diagnostic{{
			Rel:  target.yamlPath,
			Line: 1,
			Message: "L2+ cell " + target.cellID +
				": missing Init method on *" + target.goStructName +
				" — every L2+ cell needs an Init that calls kernel/cell.CheckNotNoop",
		}}
	}
	if !initReachesCheckNotNoop(p, initFn) {
		initPos := p.Fset.Position(initFn.Pos())
		initSite := fmt.Sprintf("%s:%d", initPos.Filename, initPos.Line)
		return []Diagnostic{{
			Rel:  target.yamlPath,
			Line: 1,
			Message: "L2+ cell " + target.cellID +
				": Init (same-package callees of *" + target.goStructName +
				".Init) does not call kernel/cell.CheckNotNoop;" +
				" add the call in Init or in a hand-written same-package hook" +
				" (e.g. initInternal) to guard durable-mode wiring [Init at " +
				initSite + "]",
		}}
	}
	return nil
}

// matchTarget returns the l2TargetCell whose pkgSuffix matches the given
// package import path, or nil if no target matches. Each cell occupies a
// unique cells/<id>/ directory, so at most one target matches per Pass.
func matchTarget(pkgPath string, targets []l2TargetCell) *l2TargetCell {
	for i := range targets {
		if strings.HasSuffix(pkgPath, targets[i].pkgSuffix) {
			return &targets[i]
		}
	}
	return nil
}

// initFuncDecl returns the FuncDecl named "Init" whose receiver type matches
// the goStructName (with or without leading "*"). The lookup walks every file
// via archtest.EachInChildren[ast.FuncDecl] which iterates only direct
// FuncDecl children of *ast.File (depth=1, structurally exact for top-level
// function declarations). receiverTypeName is the shared archtest helper
// defined in pg_repo_ambient_tx_test.go (handles both `(c *Foo)` and `(c Foo)`
// forms).
func initFuncDecl(p *Pass, goStructName string) *ast.FuncDecl {
	var found *ast.FuncDecl
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if found != nil {
				return
			}
			if fd.Recv == nil || fd.Name == nil || fd.Name.Name != "Init" {
				return
			}
			if receiverTypeName(fd) == goStructName {
				found = fd
			}
		})
		if found != nil {
			return found
		}
	}
	return nil
}

// initReachesCheckNotNoop returns true if init (or any same-package function
// transitively called from init's body) contains a CallExpr that resolves via
// *types.Info to kernel/cell.CheckNotNoop. The BFS is bounded by visiting
// each function declaration in the same package at most once.
//
// Same-package restriction is intentional (see file-level Blind-spot
// inventory item 4 + red_cross_pkg fixture): Init/initInternal direct call
// is the K#04 hand-written hook convention; cross-package indirection
// reopens a Soft-ification escape.
func initReachesCheckNotNoop(p *Pass, init *ast.FuncDecl) bool {
	if init == nil || p == nil || p.TypesInfo == nil {
		return false
	}
	// Index same-package funcs by *types.Func so callee resolution maps
	// directly into the BFS frontier.
	sameSrcByFunc := map[*types.Func]*ast.FuncDecl{}
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name == nil {
				return
			}
			obj := p.TypesInfo.Defs[fd.Name]
			fn, ok := obj.(*types.Func)
			if !ok {
				return
			}
			sameSrcByFunc[fn] = fd
		})
	}

	visited := map[*ast.FuncDecl]struct{}{}
	queue := []*ast.FuncDecl{init}
	for len(queue) > 0 {
		fd := queue[0]
		queue = queue[1:]
		if _, seen := visited[fd]; seen {
			continue
		}
		visited[fd] = struct{}{}
		if fd.Body == nil {
			continue
		}
		found := false
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if found {
				return
			}
			callee := resolveCallee(p, call.Fun)
			if callee == nil {
				return
			}
			if callee.FullName() == kernelCellCheckNotNoopFullName {
				found = true
				return
			}
			if next, ok := sameSrcByFunc[callee]; ok {
				queue = append(queue, next)
			}
		})
		if found {
			return true
		}
	}
	return false
}

// resolveCallee maps a CallExpr.Fun AST node to its *types.Func object via
// the pass's TypesInfo.Uses table. Returns nil for non-function callees
// (closures, interface method calls without a static target, etc.).
func resolveCallee(p *Pass, fun ast.Expr) *types.Func {
	if p == nil || p.TypesInfo == nil {
		return nil
	}
	var ident *ast.Ident
	switch e := fun.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return nil
	}
	obj, ok := p.TypesInfo.Uses[ident]
	if !ok {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}

// fixturePkgRoot is the testdata directory prefix common to every fixture
// sub-package below. Per-test pattern lists are derived from this root +
// fixture name so each test loads only the package(s) it asserts against.
const fixturePkgRoot = "./tools/archtest/testdata/cell_init_checknotnoop_fixtures/"

// fixturePkgs lists the patterns for the fixture sub-packages keyed by name.
// Each test passes only the subset it actually exercises — the framework's
// RunTypedFixture loads exactly the given patterns, so per-test scoping
// keeps per-test compile blast radius bounded to the fixture under test.
var fixturePkgs = map[string][]string{
	"red_l2_missing":      {fixturePkgRoot + "red_l2_missing"},
	"red_cross_pkg":       {fixturePkgRoot + "red_cross_pkg", fixturePkgRoot + "red_cross_pkg/internal/wrapcheck"},
	"boundary_l1":         {fixturePkgRoot + "boundary_l1"},
	"boundary_transitive": {fixturePkgRoot + "boundary_transitive"},
}

// fixtureTargetFor builds a synthetic l2TargetCell whose pkgSuffix points at
// a fixture sub-package. The tests bypass the Phase A YAML scan because
// archtest fixture loading does not surface cell.yaml side files through the
// production scope; instead each fixture test hand-codes its expected
// target.
func fixtureTargetFor(t *testing.T, fixtureName, goStructName string) l2TargetCell {
	t.Helper()
	rel := "tools/archtest/testdata/cell_init_checknotnoop_fixtures/" + fixtureName
	return l2TargetCell{
		cellID:       "fixture-" + fixtureName,
		goStructName: goStructName,
		yamlPath:     rel + "/cell.yaml",
		pkgSuffix:    "/" + rel,
	}
}

// TestCellInitCheckNotNoop_RedL2Missing — RED fixture: a fake L2 cell whose
// Init body (and same-package transitive callees) never invoke CheckNotNoop.
// Phase B must emit exactly one diagnostic.
func TestCellInitCheckNotNoop_RedL2Missing(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_l2_missing", "RedL2Cell")
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, fixturePkgs["red_l2_missing"],
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})
	require.Len(t, diags, 1,
		"red_l2_missing fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
}

// TestCellInitCheckNotNoop_RedCrossPkg — RED fixture: an L2 cell whose Init
// reaches CheckNotNoop only via a cross-package helper. Phase B's same-
// package BFS must reject this shape (Soft-ification escape) and emit one
// diagnostic.
func TestCellInitCheckNotNoop_RedCrossPkg(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_cross_pkg", "RedCrossPkgCell")
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, fixturePkgs["red_cross_pkg"],
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})
	require.Len(t, diags, 1,
		"red_cross_pkg fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
}

// TestCellInitCheckNotNoop_BoundaryL1 — BOUNDARY: a (synthetic) L1 cell that
// declares `consistencyLevel: L1` is dropped by Phase A and never reaches
// Phase B. This test exercises the Phase A filter path directly rather than
// the trivial "empty target list → no diagnostic" case.
//
// Note: the boundary_l1 fixture cell.yaml lives under testdata/ and is NOT
// inside ProductionScope, so collectL2PlusTargets(scope) will not surface it
// even when run end-to-end. Here we drive the filter with a synthetic
// cellYAMLSubset to assert "L1 declaration → consistencyLevelAtLeastL2 false
// → Phase A drops → Phase B sees no target → no diagnostic".
func TestCellInitCheckNotNoop_BoundaryL1(t *testing.T) {
	t.Parallel()

	// Phase A filter unit check: L1 must NOT satisfy the threshold.
	require.False(t, consistencyLevelAtLeastL2("L1"),
		"Phase A filter: L1 must be dropped before Phase B")

	// End-to-end shape: load the L1 fixture package; supply the empty
	// target list that Phase A would produce; assert zero diagnostics.
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, fixturePkgs["boundary_l1"],
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, nil)
		})
	require.Empty(t, diags,
		"boundary_l1 fixture: expected zero diagnostics with L1-dropped target list, got %+v", diags)
}

// TestCellInitCheckNotNoop_BoundaryTransitive — BOUNDARY: an L2 cell whose
// CheckNotNoop call lives in a same-package `initInternal` callee (the K#04
// hand-written hook convention). Phase B's BFS must reach it via same-
// package callee resolution.
func TestCellInitCheckNotNoop_BoundaryTransitive(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "boundary_transitive", "BoundaryTransitiveCell")
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, fixturePkgs["boundary_transitive"],
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})
	require.Empty(t, diags,
		"boundary_transitive fixture: expected zero diagnostics, got %+v", diags)
}

// -------------------------------------------------------------------------
// Reverse self-checks for the Medium grade blind-spot inventory. Each test
// asserts that the corresponding evasion shape does not appear in the
// current production AST. New production code that introducing any of these
// shapes will fail one of these tests and force a deliberate evaluation —
// the AST shape itself becomes a review signal even though Phase B does
// not detect it directly.
//
// These tests are sanity checks rather than full enforcement: each fails
// via require.Empty (not Report) so the failure message points directly at
// the offending AST sites without manufacturing a new INVARIANT anchor ID
// (which would have to satisfy INVENTORY-ANCHOR-VALID-ID-01's grammar
// `^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+(...)?$` — a "/no-reflect" suffix
// does not fit). The single canonical anchor remains
// CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 declared in the file header.
// -------------------------------------------------------------------------

// TestNoReflectCheckNotNoopInProduction asserts that no production *.go file
// invokes kernel/cell.CheckNotNoop via reflect.ValueOf(...).Call(...) — a
// shape that hides the callee from the Phase B BFS's *types.Info resolver.
// Blind-spot #1 from the file-level inventory. The check uses types-aware
// callee resolution (Medium-grade: *types.Info.Uses on the reflect.ValueOf
// CallExpr + recursive walk over Args looking for a CheckNotNoop *types.Func
// reference), not a string anchor.
func TestNoReflectCheckNotNoopInProduction(t *testing.T) {
	t.Parallel()
	violations := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if !isReflectValueOfCall(p, call) {
					return
				}
				if !argSubtreeReferencesCheckNotNoop(p, call.Args) {
					return
				}
				pos := p.Fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "reflect.ValueOf invocation references kernel/cell.CheckNotNoop" +
						" — this shape evades CELL-L2-INIT-CHECKNOTNOOP-CALLED-01's *types.Info" +
						" BFS; call CheckNotNoop directly from the cell's Init or same-package hook",
				})
			})
		}
		return out
	})
	require.Empty(t, violations,
		"reverse self-check (reflect blind-spot): unexpected reflect.ValueOf(... CheckNotNoop ...) in production AST; "+
			"each entry below documents an evasion site that bypasses Phase B's *types.Info BFS: %+v",
		violations)
}

// isReflectValueOfCall reports whether call.Fun resolves to reflect.ValueOf.
func isReflectValueOfCall(p *Pass, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ValueOf" {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj := p.TypesInfo.Uses[pkgIdent]
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == "reflect"
}

// argSubtreeReferencesCheckNotNoop reports whether any Ident within args
// resolves to kernel/cell.CheckNotNoop via *types.Info.Uses.
func argSubtreeReferencesCheckNotNoop(p *Pass, args []ast.Expr) bool {
	for _, arg := range args {
		hit := false
		EachInSubtree[ast.Ident](arg, func(id *ast.Ident) {
			if hit {
				return
			}
			fn, _ := p.TypesInfo.Uses[id].(*types.Func)
			if fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName {
				hit = true
			}
		})
		if hit {
			return true
		}
	}
	return false
}

// TestNoLinknameAliasInProduction asserts that no production *.go file
// declares a `//go:linkname` directive containing the literal token
// `CheckNotNoop`. Blind-spot #2 from the file-level inventory.
//
// AI-rebust note: this check is Soft (string anchor). `//go:linkname` is a
// Go compiler directive — it does NOT produce go/types symbols, so a
// types-aware probe equivalent to TestNoReflectCheckNotNoopInProduction is
// not technically reachable. The Soft form is the upper bound for the
// `//go:linkname` shape. An evader can sidestep the check by aliasing the
// linkname target under a name that omits "CheckNotNoop" (e.g.
// `myDurabilityGuard`); such evasion remains undetected. We acknowledge
// this acceptance via backlog entry LINKNAME-CHECK-SOFT-ACKNOWLEDGED-01 in
// docs/backlog/cap-14-tooling.md so reviewers do not re-file the same
// limitation.
func TestNoLinknameAliasInProduction(t *testing.T) {
	t.Parallel()
	scope := ModuleScope(findModuleRoot(t))
	var violations []Diagnostic
	EachContentFile(t, scope, []string{".go"}, func(_ *testing.T, fc ContentContext) {
		// `//go:linkname <localname> <target>` — flag any line whose target
		// segment ends in `.CheckNotNoop` or whose localname is `CheckNotNoop`.
		text := string(fc.Bytes)
		lines := strings.Split(text, "\n")
		for i, ln := range lines {
			trim := strings.TrimSpace(ln)
			if !strings.HasPrefix(trim, "//go:linkname") {
				continue
			}
			if !strings.Contains(trim, "CheckNotNoop") {
				continue
			}
			violations = append(violations, Diagnostic{
				Rel:  fc.Rel,
				Line: i + 1,
				Message: "//go:linkname directive references CheckNotNoop —" +
					" linkname aliasing evades CELL-L2-INIT-CHECKNOTNOOP-CALLED-01's BFS;" +
					" call kernel/cell.CheckNotNoop by its canonical name from the cell package",
			})
		}
	})
	require.Empty(t, violations,
		"reverse self-check (linkname blind-spot, Soft form — see LINKNAME-CHECK-SOFT-ACKNOWLEDGED-01): "+
			"unexpected //go:linkname directive referencing CheckNotNoop: %+v",
		violations)
}

// TestNoAsyncCheckNotNoopInProduction asserts that no `go func(){...}()`
// statement anywhere in an L2+ cell package contains a CallExpr that resolves
// to kernel/cell.CheckNotNoop. Blind-spot #3 from the file-level inventory.
//
// Scope honesty: the check scans EVERY GoStmt in the cell package, not just
// those statically reachable from Init's same-package callee set. This is
// an intentional over-approximation — calling CheckNotNoop asynchronously
// anywhere in the cell package (background workers, deferred goroutines,
// or Init-adjacent helpers) violates the synchronous durability-guard
// contract because the runtime guarantee depends on the check happening
// before Init returns. The diagnostic message acknowledges the broader
// scope so a reviewer who chases the report does not assume the call is
// definitely on the Init reachable path.
//
// Current production cells (accesscore / auditcore / configcore) ship no
// `go func` statements inside their main package — the only goroutines in
// those cell trees live under `*_test.go` (excluded by Tests: false) or
// inside sub-packages like `internal/ports/conformance` (different package
// path, not matched by matchTarget). So this check is currently green and
// serves as a forward-looking guard against future cell additions.
func TestNoAsyncCheckNotNoopInProduction(t *testing.T) {
	t.Parallel()
	scope := ModuleScope(findModuleRoot(t))
	targets := collectL2PlusTargets(t, scope)
	require.NotEmpty(t, targets)

	violations := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Pkg == nil {
			return nil
		}
		target := matchTarget(p.Pkg.Path(), targets)
		if target == nil {
			return nil
		}
		initFn := initFuncDecl(p, target.goStructName)
		if initFn == nil {
			return nil
		}
		// Over-approximate: scan all same-package GoStmts. False positives
		// (goroutine outside Init reachable set) are still worth surfacing
		// because the synchronous contract applies package-wide.
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.GoStmt](f, func(gostmt *ast.GoStmt) {
				if !goStmtCallsCheckNotNoop(p, gostmt) {
					return
				}
				pos := p.Fset.Position(gostmt.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "L2+ cell " + target.cellID + ": found `go func() { ... CheckNotNoop ... }()` in package" +
						" (may be outside Init reachable set; verify Init path is synchronous —" +
						" CheckNotNoop must be invoked before Init returns to guard durable-mode wiring)",
				})
			})
		}
		return out
	})
	require.Empty(t, violations,
		"reverse self-check (async blind-spot): unexpected `go func` containing CheckNotNoop in L2+ cell package: %+v",
		violations)
}

// goStmtCallsCheckNotNoop reports whether the goroutine body (or any
// expression in the go-stmt call chain) contains a CallExpr resolving to
// kernel/cell.CheckNotNoop.
func goStmtCallsCheckNotNoop(p *Pass, gostmt *ast.GoStmt) bool {
	hit := false
	EachInSubtree[ast.CallExpr](gostmt, func(call *ast.CallExpr) {
		if hit {
			return
		}
		fn := resolveCallee(p, call.Fun)
		if fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName {
			hit = true
		}
	})
	return hit
}
