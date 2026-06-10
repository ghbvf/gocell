//go:build archtest

// INVARIANT: CELL-L2-INIT-CHECKNOTNOOP-CALLED-01
//
// # CELL-L2-INIT-CHECKNOTNOOP-CALLED-01
//
// Every cell whose cell.yaml declares consistencyLevel >= "L2" MUST have its
// `(*GoStructName).Init` method body — or a transitive callee defined in the
// same Go package — invoke `kernel/outbox.CheckNotNoop` at least once. Missing
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
// This archtest protects against future L2+ cells forgetting the CheckNotNoop call.
//
// AI-robust grade: Medium (type-aware AST funnel via Run(t, Production(...)) +
// *types.Info callee resolution; scoped to production packages).
//
// # Algorithm
//
// Phase A — enumerate targets:
//
//	EachContentFile over cells/**/cell.yaml; parseAndIncludeTarget runs
//	yaml.Unmarshal into a minimal struct {ID, ConsistencyLevel, GoStructName}.
//	Cells with ConsistencyLevel >= "L2" and a non-empty GoStructName become
//	l2TargetCell entries whose `pkgPath` is the FULL import path
//	`modPath + "/" + cellDir`. L2+ cells WITHOUT goStructName emit a
//	diagnostic ("missing goStructName" — K#04 codegen convention violation)
//	instead of silently skipping. collectL2PlusTargets returns both lists.
//
// Phase B — scan production code per target:
//
//	Run(t, Production(...)) loads the production package set. matchTarget
//	compares Pass.Pkg.Path() to target.pkgPath via `==` (exact equality,
//	not HasSuffix — F4 fix prevents examples/foo/cells/<id> from being
//	mis-attributed). For the matched target, locate the Init FuncDecl on
//	`*GoStructName`, then BFS over same-package callees of Init.
//	collectFuncLitRanges + posInsideAnyRange filter out CallExprs nested
//	inside unexecuted FuncLit closures (F1 fix; equivalent to
//	inspector.Nodes returning proceed=false at FuncLit). A target
//	satisfies the rule if any visited CallExpr resolves via *types.Info
//	to `kernel/outbox.CheckNotNoop`. Diagnostics reference the cell.yaml
//	path; the does-not-call branch also embeds the Init Go file:line.
//
// # Blind-spot inventory
//
// The Medium grade is bounded by the following AST shapes that the Phase B
// scanner does NOT detect; each is paired with a reverse-self-check test in
// this file asserting the shape does not appear in current production AST:
//
//  1. `reflect.ValueOf(outbox.CheckNotNoop).Call(...)`. Reflective dispatch
//     hides the callee from *types.Info. Reverse check:
//     TestNoReflectCheckNotNoopInProduction.
//
//  2. `//go:linkname` aliasing `kernel/outbox.CheckNotNoop` under another name.
//     ACKNOWLEDGED RESIDUAL RISK — no enforcement. A string-anchor probe
//     was rejected per ai-robust.md §Review checklist ("Soft 新引入直接
//     reject"); `//go:linkname` is a compiler directive that produces no
//     go/types symbol, so a Hard or Medium probe is unreachable. Linkname
//     directives are vanishingly rare in production Go code; this blind-
//     spot is accepted as documented residual risk.
//
//  3. `go func() { outbox.CheckNotNoop(...) }()` — async dispatch from the
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
// ref: AI-robust §载体决策原则 in .claude/rules/gocell/ai-robust.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// cellInitCheckNotNoopRuleID is the diagnostic anchor for this archtest.
const cellInitCheckNotNoopRuleID = "CELL-L2-INIT-CHECKNOTNOOP-CALLED-01"

// kernelCellCheckNotNoopFullName aliases the package-level checkNotNoopFullName
// constant (declared in cell_init_checknotnoop.go) so existing test helpers in
// this file remain readable without a rename — both refer to the same
// platform-anchored path derived from PlatformModulePath.
//
// This alias removes the bare "github.com/ghbvf/gocell" literal from this
// _test.go file; the single source is now cell_init_checknotnoop.go.
const kernelCellCheckNotNoopFullName = checkNotNoopFullName

// l2TargetCell captures the minimal data Phase A collects from cell.yaml to
// drive Phase B's production scan.
type l2TargetCell struct {
	cellID       string // metadata ID
	goStructName string // CamelCase Go type, declared in cell.yaml.goStructName
	yamlPath     string // path/to/cells/<id>/cell.yaml (project-relative)
	// pkgPath is the FULL Go import path of the cell package, equal to
	// `<modulePath>/<yamlDir>`. Phase B's matchTarget compares Pass.Pkg.Path()
	// with `==`, not HasSuffix — this avoids the failure mode where
	// `examples/foo/cells/configcore` (an example cell) collides with the
	// platform cell suffix `/cells/configcore`. The repo-internal convention
	// for package-path matching is exact-equality on Pkg.Path() (see
	// `tools/archtest/baseslice_ctor_funnel_01_test.go:82,109` and
	// `tools/archtest/adapter_error_classification_test.go:87,140`).
	// ref: go/types.Package.Path returns the canonical import path.
	pkgPath string
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

// TestParseAndIncludeTarget exercises the Phase A pure helper on every
// fixture cell.yaml under testdata/cell_init_checknotnoop_fixtures/. Each
// case loads the YAML file via os.ReadFile (bypassing scope-based walking
// so testdata-scoped YAML can drive the test), invokes parseAndIncludeTarget,
// and asserts the (target, diagnostic, include) tuple matches expectations.
//
// This closes F6 from the round-2 review: prior to this test the Phase A
// branches (L0/L1 drop, L2+ accept, L2+ missing-goStructName diagnostic)
// were only exercised end-to-end through ProductionScope, which excludes
// testdata/. fixture cell.yaml files could drift relative to their cell.go
// siblings without any test failing. With this YAML-driven unit test, any
// change to a fixture cell.yaml's id / consistencyLevel / goStructName is
// immediately observed.
func TestParseAndIncludeTarget(t *testing.T) {
	t.Parallel()
	modPath := fixtureModPath(t)
	root := findModuleRoot(t)

	cases := []struct {
		name           string
		rel            string // module-relative path to fixture cell.yaml
		wantInclude    bool
		wantDiag       bool
		wantCellID     string
		wantGoStruct   string
		wantDiagSubstr string
	}{
		{
			name:         "L2 with goStructName — red_l2_missing",
			rel:          fixturePkgRoot[2:] + "red_l2_missing/cell.yaml", // strip leading "./"
			wantInclude:  true,
			wantCellID:   "fixture-red_l2_missing",
			wantGoStruct: "RedL2Cell",
		},
		{
			name:         "L2 with goStructName — red_cross_pkg",
			rel:          fixturePkgRoot[2:] + "red_cross_pkg/cell.yaml",
			wantInclude:  true,
			wantCellID:   "fixture-red_cross_pkg",
			wantGoStruct: "RedCrossPkgCell",
		},
		{
			name:         "L2 with goStructName — red_funclit_only",
			rel:          fixturePkgRoot[2:] + "red_funclit_only/cell.yaml",
			wantInclude:  true,
			wantCellID:   "fixture-red_funclit_only",
			wantGoStruct: "RedFuncLitCell",
		},
		{
			name:         "L2 with goStructName — red_missing_init",
			rel:          fixturePkgRoot[2:] + "red_missing_init/cell.yaml",
			wantInclude:  true,
			wantCellID:   "fixture-red_missing_init",
			wantGoStruct: "RedMissingInitCell",
		},
		{
			name:         "L2 with goStructName — boundary_transitive",
			rel:          fixturePkgRoot[2:] + "boundary_transitive/cell.yaml",
			wantInclude:  true,
			wantCellID:   "fixture-boundary_transitive",
			wantGoStruct: "BoundaryTransitiveCell",
		},
		{
			name:        "L1 dropped — boundary_l1",
			rel:         fixturePkgRoot[2:] + "boundary_l1/cell.yaml",
			wantInclude: false,
			wantDiag:    false,
		},
		{
			name:           "L2 missing goStructName — diagnostic emitted",
			rel:            fixturePkgRoot[2:] + "missing_struct_name/cell.yaml",
			wantInclude:    false,
			wantDiag:       true,
			wantDiagSubstr: "missing goStructName",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// #nosec G304 -- tc.rel is a hard-coded module-relative path
			// from the test's own case list (not user input); this is
			// test-only fixture-yaml reading, mirrors the pattern in
			// tools/archtest/internal/scanner/content.go:51.
			content, err := os.ReadFile(filepath.Join(root, tc.rel))
			require.NoError(t, err, "read fixture cell.yaml %s", tc.rel)

			target, diag, ok := parseAndIncludeTarget(t, tc.rel, content, modPath)

			require.Equal(t, tc.wantInclude, ok, "include flag mismatch for %s", tc.rel)

			if tc.wantDiag {
				require.NotNil(t, diag, "expected diagnostic for %s", tc.rel)
				require.Contains(t, diag.Message, tc.wantDiagSubstr,
					"diagnostic message substring mismatch: got %q", diag.Message)
				require.Equal(t, tc.rel, diag.Rel, "diagnostic Rel must point at fixture yaml")
			} else {
				require.Nil(t, diag, "unexpected diagnostic for %s: %+v", tc.rel, diag)
			}

			if tc.wantInclude {
				require.Equal(t, tc.wantCellID, target.cellID)
				require.Equal(t, tc.wantGoStruct, target.goStructName)
				require.Equal(t, tc.rel, target.yamlPath)
				wantPkgPath := modPath + "/" + filepath.ToSlash(filepath.Dir(tc.rel))
				require.Equal(t, wantPkgPath, target.pkgPath,
					"target.pkgPath must equal modPath + cellDir for F4 == matching")
			}
		})
	}
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

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	scope := ModuleScope(root)
	targets, missingDiags := collectL2PlusTargets(t, scope, modPath)
	require.NotEmpty(t, targets,
		"expected at least one L2+ cell in cells/** — has the platform cell layout changed?")

	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		return scanCellsForInitCheckNotNoop(p, targets)
	})

	diags = append(diags, missingDiags...)

	Report(t, cellInitCheckNotNoopRuleID, diags)
}

// collectL2PlusTargets walks cell.yaml files under scope, parses the minimal
// subset, and returns one l2TargetCell per L2+ cell. The second return is a
// slice of diagnostics for L2+ cells that declare `consistencyLevel >= L2`
// but omit `goStructName` — K#04 codegen convention requires goStructName
// for the archtest to locate the Init receiver, so a missing value is itself
// a rule violation rather than a silent skip (F2 fix in PR #576 round-2).
//
// Scope filtering: only cell.yaml files whose module-relative path starts
// with "cells/" are considered. `examples/<name>/cells/<id>/cell.yaml`
// (CLAUDE.md allows examples to ship their own cells) does NOT start with
// "cells/" and is intentionally skipped — the rule guards the platform
// cell layout only; example cells are demonstration scaffolding outside
// the deployed platform surface.
func collectL2PlusTargets(t *testing.T, scope Scope, modPath string) ([]l2TargetCell, []Diagnostic) {
	t.Helper()

	var (
		targets    []l2TargetCell
		missingGSN []Diagnostic
	)
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
		target, diag, ok := parseAndIncludeTarget(t, rel, fc.Bytes, modPath)
		if diag != nil {
			missingGSN = append(missingGSN, *diag)
		}
		if ok {
			targets = append(targets, target)
		}
	})
	return targets, missingGSN
}

// parseAndIncludeTarget is the pure helper consumed by collectL2PlusTargets.
// It parses content into cellYAMLSubset, applies the L2+ filter, and:
//
//   - returns (target, nil, true) for L2+ cells with non-empty goStructName
//   - returns (zero, &diag, false) for L2+ cells whose goStructName is empty
//     (F2 fix: missing goStructName is a rule violation, not silent skip)
//   - returns (zero, nil, false) for cells below the L2 threshold
//
// Extracted to a pure helper to keep YAML parse + filter testable
// independently of EachContentFile scope walking.
func parseAndIncludeTarget(t *testing.T, rel string, content []byte, modPath string) (l2TargetCell, *Diagnostic, bool) {
	t.Helper()
	var meta cellYAMLSubset
	if err := yaml.Unmarshal(content, &meta); err != nil {
		t.Fatalf("cell.yaml parse %s: %v", rel, err)
	}
	if !consistencyLevelAtLeastL2(meta.ConsistencyLevel) {
		return l2TargetCell{}, nil, false
	}
	if meta.GoStructName == "" {
		return l2TargetCell{}, &Diagnostic{
			Rel:  rel,
			Line: 1,
			Message: "L2+ cell " + meta.ID +
				": cell.yaml is missing goStructName — K#04 codegen convention" +
				" requires L2+ cells to declare goStructName so this archtest" +
				" can locate the Init method receiver",
		}, false
	}
	cellDir := filepath.Dir(rel) // "cells/<id>"
	return l2TargetCell{
		cellID:       meta.ID,
		goStructName: meta.GoStructName,
		yamlPath:     rel,
		pkgPath:      modPath + "/" + cellDir,
	}, nil, true
}

// scanCellsForInitCheckNotNoop runs Phase B on a single Pass. It selects the
// l2TargetCell whose pkgPath equals Pass.Pkg.Path() (at most one match),
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
				" — every L2+ cell needs an Init that calls kernel/outbox.CheckNotNoop",
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
				".Init) does not call kernel/outbox.CheckNotNoop;" +
				" add the call in Init or in a hand-written same-package hook" +
				" (e.g. initInternal) to guard durable-mode wiring [Init at " +
				initSite + "]",
		}}
	}
	return nil
}

// matchTarget returns the l2TargetCell whose full import path equals
// `pkgPath`, or nil if no target matches. Exact equality (not HasSuffix)
// prevents `examples/foo/cells/configcore` from being mis-attributed as
// the platform `configcore` cell — F4 fix in PR #576 round-2 review.
//
// ref: tools/archtest/baseslice_ctor_funnel_01_test.go:82,109
//
//	(repo convention: cellPkgPath built as modPath + "/kernel/cell",
//	compared with == against p.Pkg.Path()).
//
// ref: tools/archtest/adapter_error_classification_test.go:87,140
//
//	(same exact-equality pattern on p.Pkg.Path()).
//
// ref: go/types.Package.Path returns the canonical import path; exact
//
//	string comparison is the standard library convention.
func matchTarget(pkgPath string, targets []l2TargetCell) *l2TargetCell {
	for i := range targets {
		if targets[i].pkgPath == pkgPath {
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
// *types.Info to kernel/outbox.CheckNotNoop **outside any nested FuncLit**.
// The BFS is bounded by visiting each function declaration in the same
// package at most once.
//
// FuncLit boundary semantics: CallExprs inside a nested function literal
// (closure / lambda) are NOT counted, because such a closure may be
// constructed without ever being invoked — the synchronous Init contract
// requires the guard call to execute before Init returns. This mirrors
// `golang.org/x/tools/go/ast/inspector.Nodes` returning `proceed=false`
// on a FuncLit push event. archtest's EachInSubtree[T] does not expose
// boundary control directly, so the implementation pre-collects FuncLit
// position ranges and filters CallExprs that fall inside any range —
// equivalent in outcome, more indirect in mechanism.
//
// Same-package restriction is intentional (see file-level Blind-spot
// inventory item 4 + red_cross_pkg fixture): Init/initInternal direct call
// is the K#04 hand-written hook convention; cross-package indirection
// reopens a Soft-ification escape.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes proceed semantics
// ref: tools/archtest/walk.go EachInSubtree (no native boundary control)
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
		funcLitRanges := collectFuncLitRanges(fd.Body)
		_, found := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
			if posInsideAnyRange(call.Pos(), funcLitRanges) {
				return false // CallExpr nested in unexecuted FuncLit — boundary skip
			}
			callee := resolveCallee(p, call.Fun)
			if callee == nil {
				return false
			}
			if callee.FullName() == kernelCellCheckNotNoopFullName {
				return true
			}
			if next, ok := sameSrcByFunc[callee]; ok {
				queue = append(queue, next)
			}
			return false
		})
		if found {
			return true
		}
	}
	return false
}

// funcLitRange records the byte position span of a single FuncLit node;
// used by initReachesCheckNotNoop and argSubtreeReferencesCheckNotNoop to
// emulate boundary-controlled AST walking on archtest's EachInSubtree[T].
type funcLitRange struct {
	start, end token.Pos
}

// collectFuncLitRanges returns the position spans of every FuncLit reachable
// in root's subtree. Used as the "exclude inside these spans" filter so the
// outer CallExpr walk skips CallExprs that belong to a nested unexecuted
// closure. Equivalent to inspector.Nodes returning proceed=false at FuncLit.
func collectFuncLitRanges(root ast.Node) []funcLitRange {
	var ranges []funcLitRange
	EachInSubtree[ast.FuncLit](root, func(fl *ast.FuncLit) {
		ranges = append(ranges, funcLitRange{start: fl.Pos(), end: fl.End()})
	})
	return ranges
}

// posInsideAnyRange reports whether pos falls strictly between the start
// and end of any range. token.Pos values are byte offsets within the shared
// token.FileSet, so simple numeric comparison is sufficient.
func posInsideAnyRange(pos token.Pos, ranges []funcLitRange) bool {
	for _, r := range ranges {
		if pos > r.start && pos < r.end {
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
// Run(t, Fixture(...)) loads exactly the given patterns, so per-test scoping
// keeps per-test compile blast radius bounded to the fixture under test.
var fixturePkgs = map[string][]string{
	"red_l2_missing":      {fixturePkgRoot + "red_l2_missing"},
	"red_cross_pkg":       {fixturePkgRoot + "red_cross_pkg", fixturePkgRoot + "red_cross_pkg/internal/wrapcheck"},
	"red_funclit_only":    {fixturePkgRoot + "red_funclit_only"},
	"red_missing_init":    {fixturePkgRoot + "red_missing_init"},
	"boundary_l1":         {fixturePkgRoot + "boundary_l1"},
	"boundary_transitive": {fixturePkgRoot + "boundary_transitive"},
}

// fixtureTargetFor builds a synthetic l2TargetCell whose pkgPath points at
// a fixture sub-package. The tests bypass the Phase A YAML scan because
// archtest fixture loading does not surface cell.yaml side files through the
// production scope; instead each fixture test hand-codes its expected
// target using the same `<modPath>/<rel>` import-path shape as production
// cells (F4 round-2 fix: exact-equality matchTarget requires full path).
func fixtureTargetFor(t *testing.T, fixtureName, goStructName, modPath string) l2TargetCell {
	t.Helper()
	rel := "tools/archtest/testdata/cell_init_checknotnoop_fixtures/" + fixtureName
	return l2TargetCell{
		cellID:       "fixture-" + fixtureName,
		goStructName: goStructName,
		yamlPath:     rel + "/cell.yaml",
		pkgPath:      modPath + "/" + rel,
	}
}

// fixtureModPath is the cached module import path used by the fixture
// tests; resolved once from go.mod via moduleImportPath.
func fixtureModPath(t *testing.T) string {
	t.Helper()
	mp, err := moduleImportPath(findModuleRoot(t))
	require.NoError(t, err, "fixtureModPath: read module path from go.mod")
	return mp
}

// TestCellInitCheckNotNoop_RedL2Missing — RED fixture: a fake L2 cell whose
// Init body (and same-package transitive callees) never invoke CheckNotNoop.
// Phase B must emit exactly one diagnostic whose message identifies the
// "does not call kernel/outbox.CheckNotNoop" branch (F5 round-2 fix:
// substring assertion distinguishes this branch from the "missing Init"
// branch — see TestCellInitCheckNotNoop_RedMissingInit).
func TestCellInitCheckNotNoop_RedL2Missing(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_l2_missing", "RedL2Cell", fixtureModPath(t))
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["red_l2_missing"]),
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})

	require.Len(t, diags, 1,
		"red_l2_missing fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
	require.Contains(t, diags[0].Message, "does not call kernel/outbox.CheckNotNoop",
		"red_l2_missing: diagnostic must identify the does-not-call branch, got %q", diags[0].Message)
}

// TestCellInitCheckNotNoop_RedCrossPkg — RED fixture: an L2 cell whose Init
// reaches CheckNotNoop only via a cross-package helper. Phase B's same-
// package BFS must reject this shape (Soft-ification escape) and emit one
// diagnostic on the "does not call" branch (same as red_l2_missing — the
// cross-package callee is not reachable from same-package BFS, so the
// outcome is identical from Phase B's perspective).
func TestCellInitCheckNotNoop_RedCrossPkg(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_cross_pkg", "RedCrossPkgCell", fixtureModPath(t))
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["red_cross_pkg"]),
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})

	require.Len(t, diags, 1,
		"red_cross_pkg fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
	require.Contains(t, diags[0].Message, "does not call kernel/outbox.CheckNotNoop",
		"red_cross_pkg: diagnostic must identify the does-not-call branch, got %q", diags[0].Message)
}

// TestCellInitCheckNotNoop_RedMissingInit — RED fixture: an L2 cell whose
// cell.yaml declares goStructName but provides no Init method. Phase B's
// initFuncDecl returns nil, and scanCellsForInitCheckNotNoop must emit
// the "missing Init method" branch (distinct from "does not call"). F5
// fix: substring assertion distinguishes the two diagnostic branches so
// future regressions where a receiver rename hides Init don't masquerade
// as the does-not-call branch.
func TestCellInitCheckNotNoop_RedMissingInit(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_missing_init", "RedMissingInitCell", fixtureModPath(t))
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["red_missing_init"]),
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})

	require.Len(t, diags, 1,
		"red_missing_init fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
	require.Contains(t, diags[0].Message, "missing Init method",
		"red_missing_init: diagnostic must identify the missing-Init branch, got %q", diags[0].Message)
}

// TestCellInitCheckNotNoop_RedFuncLitOnly — RED fixture: an L2 cell whose
// CheckNotNoop call only appears inside an unexecuted closure (FuncLit).
// Phase B's BFS must stop at FuncLit boundaries (mirroring inspector.Nodes
// proceed=false on FuncLit push), so this cell is reported as "does not
// call CheckNotNoop". Without the F1 fix, the deep walk would falsely
// register the closure-internal call.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes proceed semantics
func TestCellInitCheckNotNoop_RedFuncLitOnly(t *testing.T) {
	t.Parallel()
	target := fixtureTargetFor(t, "red_funclit_only", "RedFuncLitCell", fixtureModPath(t))
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["red_funclit_only"]),
		func(p *Pass) []Diagnostic {
			return scanCellsForInitCheckNotNoop(p, []l2TargetCell{target})
		})

	require.Len(t, diags, 1,
		"red_funclit_only fixture: expected one diagnostic, got %d (%+v)", len(diags), diags)
	require.Contains(t, diags[0].Message, "does not call kernel/outbox.CheckNotNoop",
		"red_funclit_only: diagnostic must identify the does-not-call branch, got %q", diags[0].Message)
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
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["boundary_l1"]),
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
	target := fixtureTargetFor(t, "boundary_transitive", "BoundaryTransitiveCell", fixtureModPath(t))
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, fixturePkgs["boundary_transitive"]),
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
// invokes kernel/outbox.CheckNotNoop via reflect.ValueOf(...).Call(...) — a
// shape that hides the callee from the Phase B BFS's *types.Info resolver.
// Blind-spot #1 from the file-level inventory. The check uses types-aware
// callee resolution (Medium-grade: *types.Info.Uses on the reflect.ValueOf
// CallExpr + recursive walk over Args looking for a CheckNotNoop *types.Func
// reference), not a string anchor.
func TestNoReflectCheckNotNoopInProduction(t *testing.T) {
	t.Parallel()
	violations := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
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
					Message: "reflect.ValueOf invocation references kernel/outbox.CheckNotNoop" +
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
// resolves to kernel/outbox.CheckNotNoop via *types.Info.Uses, **excluding**
// Idents that fall inside a nested FuncLit body (F1 fix: a closure passed
// as argument to reflect.ValueOf which itself happens to contain a
// CheckNotNoop reference should not be flagged unless reflect.ValueOf's
// direct argument is the bare function reference). FuncLit boundary
// semantics mirror initReachesCheckNotNoop.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes proceed semantics
func argSubtreeReferencesCheckNotNoop(p *Pass, args []ast.Expr) bool {
	for _, arg := range args {
		funcLitRanges := collectFuncLitRanges(arg)
		_, hit := FindFirstInSubtree[ast.Ident](arg, func(id *ast.Ident) bool {
			if posInsideAnyRange(id.Pos(), funcLitRanges) {
				return false // Ident nested in FuncLit body — boundary skip
			}
			fn, _ := p.TypesInfo.Uses[id].(*types.Func)
			return fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName
		})
		if hit {
			return true
		}
	}
	return false
}

// (TestNoLinknameAliasInProduction REMOVED in PR #576 round-2 review:
// `.claude/rules/gocell/ai-robust.md` §Review checklist mandates that
// newly-introduced Soft enforcement be rejected outright. The probe was a
// `strings.Contains` anchor on `//go:linkname` directives — Soft by
// definition. The blind-spot is now documented as acknowledged residual
// risk in the file-header godoc and PR body; no CI enforcement remains.)

// TestNoAsyncCheckNotNoopInProduction asserts that no `go func(){...}()`
// statement anywhere in an L2+ cell package contains a CallExpr that resolves
// to kernel/outbox.CheckNotNoop. Blind-spot #3 from the file-level inventory.
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
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	scope := ModuleScope(root)
	targets, _ := collectL2PlusTargets(t, scope, modPath)
	require.NotEmpty(t, targets)

	violations := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
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
// kernel/outbox.CheckNotNoop.
func goStmtCallsCheckNotNoop(p *Pass, gostmt *ast.GoStmt) bool {
	_, hit := FindFirstInSubtree[ast.CallExpr](gostmt, func(call *ast.CallExpr) bool {
		fn := resolveCallee(p, call.Fun)
		return fn != nil && fn.FullName() == kernelCellCheckNotNoopFullName
	})
	return hit
}
