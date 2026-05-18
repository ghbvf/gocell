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
// ref: kernel/cell/durability.go (CheckNotNoop, Nooper)
// ref: AI-rebust §载体决策原则 in .claude/rules/gocell/ai-collab.md
package archtest

import (
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
// L2+ threshold. "L0"-"L4" are lexicographically ordered identically to their
// semantic ordering — string compare suffices and avoids a parallel ordinal
// helper. Empty strings (omitted level) do not meet the threshold.
func consistencyLevelAtLeastL2(level string) bool {
	return level >= "L2" && level <= "L4"
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
// Diagnostics reference the target's yamlPath at line 1 — the rule is about
// a missing wiring on the cell as a whole; Init's Go file position is less
// informative than "this cell declared L2+ in cell.yaml but its Init does
// not call CheckNotNoop".
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
		return []Diagnostic{{
			Rel:  target.yamlPath,
			Line: 1,
			Message: "L2+ cell " + target.cellID +
				": Init (same-package callees of *" + target.goStructName +
				".Init) does not call kernel/cell.CheckNotNoop;" +
				" add the call in Init or in a hand-written same-package hook" +
				" (e.g. initInternal) to guard durable-mode wiring",
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
// in the pass; receiverTypeName is the shared archtest helper defined in
// pg_repo_ambient_tx_test.go (handles both `(c *Foo)` and `(c Foo)` forms).
func initFuncDecl(p *Pass, goStructName string) *ast.FuncDecl {
	for _, f := range p.Files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name == nil || fd.Name.Name != "Init" {
				continue
			}
			if receiverTypeName(fd) == goStructName {
				return fd
			}
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
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			obj := p.TypesInfo.Defs[fd.Name]
			fn, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sameSrcByFunc[fn] = fd
		}
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
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := resolveCallee(p, call.Fun)
			if callee == nil {
				return true
			}
			if callee.FullName() == kernelCellCheckNotNoopFullName {
				found = true
				return false
			}
			if next, ok := sameSrcByFunc[callee]; ok {
				queue = append(queue, next)
			}
			return true
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

// cellInitCheckNotNoopFixturePkgs lists the fixture sub-packages used by the
// RED/BOUNDARY tests below. Each package lives under
// `tools/archtest/testdata/cell_init_checknotnoop_fixtures/<name>/` with a
// `//go:build archtest_fixture` directive and a cell.yaml sibling.
var cellInitCheckNotNoopFixturePkgs = []string{
	"./tools/archtest/testdata/cell_init_checknotnoop_fixtures/red_l2_missing",
	"./tools/archtest/testdata/cell_init_checknotnoop_fixtures/red_cross_pkg",
	"./tools/archtest/testdata/cell_init_checknotnoop_fixtures/red_cross_pkg/internal/wrapcheck",
	"./tools/archtest/testdata/cell_init_checknotnoop_fixtures/boundary_l1",
	"./tools/archtest/testdata/cell_init_checknotnoop_fixtures/boundary_transitive",
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
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, cellInitCheckNotNoopFixturePkgs,
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
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, cellInitCheckNotNoopFixturePkgs,
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})
	require.Len(t, diags, 1,
		"red_cross_pkg fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
}

// TestCellInitCheckNotNoop_BoundaryL1 — BOUNDARY: an L1 cell that does not
// call CheckNotNoop is fine. The rule only applies to L2+. Phase A drops the
// target; even if a malformed caller passes it, Phase B should not emit a
// false positive when invoked with an empty target list.
func TestCellInitCheckNotNoop_BoundaryL1(t *testing.T) {
	t.Parallel()
	// L1 targets are dropped by Phase A — supply empty target list to mirror
	// the production rule's behavior on this fixture.
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, cellInitCheckNotNoopFixturePkgs,
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, nil)
		})
	require.Empty(t, diags,
		"boundary_l1 fixture: expected zero diagnostics, got %+v", diags)
}

// TestCellInitCheckNotNoop_BoundaryTransitive — BOUNDARY: an L2 cell whose
// CheckNotNoop call lives in a same-package `initInternal` callee (the K#04
// hand-written hook convention). Phase B's BFS must reach it via same-
// package callee resolution.
func TestCellInitCheckNotNoop_BoundaryTransitive(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "boundary_transitive", "BoundaryTransitiveCell")
	diags := RunTypedFixture(t, FixtureOpts{Tests: false}, cellInitCheckNotNoopFixturePkgs,
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})
	require.Empty(t, diags,
		"boundary_transitive fixture: expected zero diagnostics, got %+v", diags)
}

// -------------------------------------------------------------------------
// Reverse self-checks for the Medium grade blind-spot inventory. Each test
// asserts that the corresponding evasion shape does not appear in the
// current production AST. New production code that introduces any of these
// shapes will fail one of these tests and force a deliberate evaluation —
// the AST shape itself becomes a review signal even though Phase B does
// not detect it directly.
// -------------------------------------------------------------------------

// TestNoReflectCheckNotNoopInProduction asserts that no production *.go file
// invokes kernel/cell.CheckNotNoop via reflect.ValueOf(...).Call(...) — a
// shape that hides the callee from the Phase B BFS's *types.Info resolver.
// Blind-spot #1 from the file-level inventory.
func TestNoReflectCheckNotNoopInProduction(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if !isReflectValueOfCall(p, call) {
					return true
				}
				if !argSubtreeReferencesCheckNotNoop(p, call.Args) {
					return true
				}
				pos := p.Fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "reflect.ValueOf invocation references kernel/cell.CheckNotNoop" +
						" — this shape evades CELL-L2-INIT-CHECKNOTNOOP-CALLED-01's *types.Info" +
						" BFS; call CheckNotNoop directly from the cell's Init or same-package hook",
				})
				return true
			})
		}
		return out
	})
	Report(t, cellInitCheckNotNoopRuleID+"/no-reflect", diags)
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
		ast.Inspect(arg, func(n ast.Node) bool {
			if hit {
				return false
			}
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			fn, _ := p.TypesInfo.Uses[id].(*types.Func)
			if fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName {
				hit = true
				return false
			}
			return true
		})
		if hit {
			return true
		}
	}
	return false
}

// TestNoLinknameAliasInProduction asserts that no production *.go file
// declares a `//go:linkname` directive aliasing kernel/cell.CheckNotNoop.
// Blind-spot #2 from the file-level inventory. Content scan over the
// production scope (excluding tests and generated/).
func TestNoLinknameAliasInProduction(t *testing.T) {
	t.Parallel()
	scope := ModuleScope(findModuleRoot(t))
	var diags []Diagnostic
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
			diags = append(diags, Diagnostic{
				Rel:  fc.Rel,
				Line: i + 1,
				Message: "//go:linkname directive references CheckNotNoop —" +
					" linkname aliasing evades CELL-L2-INIT-CHECKNOTNOOP-CALLED-01's BFS;" +
					" call kernel/cell.CheckNotNoop by its canonical name from the cell package",
			})
		}
	})
	Report(t, cellInitCheckNotNoopRuleID+"/no-linkname", diags)
}

// TestNoAsyncCheckNotNoopInProduction asserts that no production *.go file
// invokes kernel/cell.CheckNotNoop inside a `go func(){...}()` block from
// inside the function reachable set of any L2+ cell Init. The synchronous
// Init contract demands the call happen before Init returns; an async call
// is statically reachable but may not have executed at Init's epilogue.
// Blind-spot #3 from the file-level inventory.
func TestNoAsyncCheckNotNoopInProduction(t *testing.T) {
	t.Parallel()
	scope := ModuleScope(findModuleRoot(t))
	targets := collectL2PlusTargets(t, scope)
	require.NotEmpty(t, targets)

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
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
		// Visit every FuncDecl in the same package (Init's reachable set is
		// bounded by same-package callees; for the negative probe we
		// over-approximate by scanning all same-package funcs — false
		// positives here would still be worth flagging).
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			ast.Inspect(f, func(n ast.Node) bool {
				gostmt, ok := n.(*ast.GoStmt)
				if !ok {
					return true
				}
				if !goStmtCallsCheckNotNoop(p, gostmt) {
					return true
				}
				pos := p.Fset.Position(gostmt.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "async `go func() { ... CheckNotNoop ... }()` in L2+ cell " + target.cellID +
						" — Init must call CheckNotNoop synchronously to guard durable-mode wiring",
				})
				return true
			})
		}
		return out
	})
	Report(t, cellInitCheckNotNoopRuleID+"/no-async", diags)
}

// goStmtCallsCheckNotNoop reports whether the goroutine body (or any
// expression in the go-stmt call chain) contains a CallExpr resolving to
// kernel/cell.CheckNotNoop.
func goStmtCallsCheckNotNoop(p *Pass, gostmt *ast.GoStmt) bool {
	hit := false
	ast.Inspect(gostmt, func(n ast.Node) bool {
		if hit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn := resolveCallee(p, call.Fun)
		if fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName {
			hit = true
			return false
		}
		return true
	})
	return hit
}
