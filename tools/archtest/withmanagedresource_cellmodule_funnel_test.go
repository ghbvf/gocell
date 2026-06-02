package archtest

// invariants:
//   - INVARIANT: WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01
//
// # WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01 — cellmodules must not reference bootstrap.WithManagedResource
//
// ## Rule
//
// No production file under `cellmodules/` may reference
// `runtime/bootstrap.WithManagedResource` in ANY form (call, func-value,
// import-alias, or dot-import). Cell modules declare their managed resources by
// returning them in `composition.ModuleResult.Resources` (the single-source
// channel introduced by PR #591 / #1420). The Builder
// (`runtime/composition/builder.go`) is the sole sanctioned site that turns a
// module's `Resources` into `bootstrap.WithManagedResource(r)` AND into the
// pre-Run rollback stack — deriving BOTH from one source so the two can never
// diverge (the former double-write bug).
//
// Composition roots OUTSIDE cellmodules/ (`cmd/corebundle`, `examples/iotdevice`,
// `examples/ssobff`) legitimately call WithManagedResource directly — they are
// not CellModules. The `cellmodules/` path prefix IS the scope boundary, so no
// per-caller carve-out is needed.
//
// ## AI-robust rating
//
//   - Downstream: **Hard** — the reference identity is resolved through
//     `p.TypesInfo.Uses[ident] → *types.Func → Pkg().Path()=="…/runtime/bootstrap"
//     && Name()=="WithManagedResource"`, exactly the PANIC-REGISTERED-01 /
//     TYPESUTIL-IMPLEMENTS-FUNNEL-01 type-resolution kernel. Because the Uses
//     sweep covers EVERY reference form — call `bootstrap.WithManagedResource(r)`,
//     func-value `f := bootstrap.WithManagedResource`, import-alias
//     `bs.WithManagedResource(r)`, dot-import bare `WithManagedResource(r)` —
//     there is no "looks like a reference but isn't" gray zone and no shape a
//     CallExpr-only walk would miss. (The pre-#1511 AST walk matched only
//     `CallExpr.Fun` SelectorExprs, so func-value and dot-import bypassed it;
//     this is the F3 finding that motivated the type-aware upgrade.)
//   - Upstream: **Medium** — Go cannot make "reference this exported func from a
//     package under cellmodules/" inexpressible; a future cellmodules file CAN add
//     the reference and only this archtest (CI) catches it. Same permanent ceiling
//     as the holder-seal funnels (#851 / #893 / #1282). For an exported symbol Go
//     cannot seal against a chosen importer set, archtest-bound type-resolution +
//     fail-on-deviation is the Hard ceiling for the downstream axis; the upstream
//     axis stays an honest Medium.
//
// ## Blind spots (charter §"工具选定后强制盲区自检"; each has a reverse self-check fixture)
//
//   - call form `bootstrap.WithManagedResource(r)` — red fixture `red/call.go`.
//   - func-value form `f := bootstrap.WithManagedResource` (reference NOT in
//     CallExpr.Fun position) — red fixture `red/call.go`. The Uses sweep makes
//     this a guarded case, not a blind spot.
//   - import-alias form `bs.WithManagedResource(r)` — red fixture `red/alias.go`.
//   - dot-import bare form `WithManagedResource(r)` (`import . "…/bootstrap"`) —
//     red fixture `red/dotimport.go`.
//   - homonym from a different package (`other.WithManagedResource`) — green
//     fixture `green/usage.go` proves the type-resolution discriminates by
//     Pkg().Path() and does NOT over-fire.
//   - `_test.go` files are excluded (Production scope is Tests:false AND the
//     detector skips _test.go): builder/module unit tests legitimately construct
//     WithManagedResource to assemble fakes.
//   - generated/ output excluded by Run(t, Production(...)); codegen does not emit
//     WithManagedResource references. Honest scope declaration.
//   - testdata/ RED/GREEN fixtures are excluded from the forward Production scan
//     (Go excludes testdata/ from ./...) and loaded explicitly by the reverse
//     self-check via Run(t, Typed(...)) non-recursive patterns.
//
// ref: tools/archtest/implements_funnel_test.go — the companion Hard pattern
//
//	(Uses → *types.Func identity, every reference form covered).
//
// ref: runtime/composition/builder.go — managedResourceOpts, the sole sanctioned site.

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleWithManagedResourceCellmoduleFunnel01 = "WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01"
	withManagedResourceFunc                   = "WithManagedResource"
	cellmodulesScopePrefix                    = "cellmodules/"
	// withManagedResourceFixturePrefix is the module-relative prefix of this
	// rule's reverse-self-check fixtures; the reverse test passes it as the
	// scope so the SAME detector predicate exercises the RED/GREEN fixtures.
	withManagedResourceFixturePrefix = "tools/archtest/testdata/withmanagedresource_cellmodule_fixtures/"
)

// bootstrapPkgPath ("github.com/ghbvf/gocell/runtime/bootstrap") is declared in
// probename_sealed_funnel_test.go and reused here (same package).

// collectWithManagedResourceFunnelViolations sweeps p.TypesInfo.Uses across every
// file under scopePrefix in the Pass and reports each *ast.Ident that resolves to
// the runtime/bootstrap.WithManagedResource *types.Func. Sweeping Uses (rather
// than only CallExpr.Fun) is what makes the rule Hard: call form, func-value form,
// import-alias form, and dot-import bare form all produce a Uses entry whose object
// is the same *types.Func, so no reference shape escapes.
//
// scopePrefix is the only allowlist boundary: the forward test passes
// cellmodulesScopePrefix; the reverse self-check passes the fixture prefix. The
// type-resolution predicate is identical in both — only the path scope differs,
// which is inherent to a path-scoped (not single-file-allowlist) rule.
func collectWithManagedResourceFunnelViolations(p *Pass, scopePrefix string) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var diags []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		if !strings.HasPrefix(rel, scopePrefix) {
			continue
		}
		if strings.HasSuffix(rel, "_test.go") {
			continue // module unit tests may construct WithManagedResource for fakes
		}
		EachInSubtree[ast.Ident](f, func(id *ast.Ident) {
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil {
				return
			}
			if fn.Pkg().Path() != bootstrapPkgPath || fn.Name() != withManagedResourceFunc {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(id.Pos()).Line,
				Message: "bootstrap.WithManagedResource referenced inside " + scopePrefix +
					"; cell modules must declare resources via composition.ModuleResult.Resources " +
					"(the Builder is the sole sanctioned site that calls WithManagedResource and " +
					"derives rollback from the same source).",
			})
		})
	}
	return diags
}

// TestWithManagedResourceCellmoduleFunnel01 enforces that no production file under
// cellmodules/ references bootstrap.WithManagedResource in any form. sawCellmodulesFile
// is a structural regression guard: if a future refactor renames the cellmodules/
// tree, the scan would otherwise pass vacuously green.
func TestWithManagedResourceCellmoduleFunnel01(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	sawCellmodulesFile := false

	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasPrefix(rel, cellmodulesScopePrefix) && !strings.HasSuffix(rel, "_test.go") {
				sawCellmodulesFile = true
			}
		}
		violations = append(violations, collectWithManagedResourceFunnelViolations(p, cellmodulesScopePrefix)...)
		return nil
	})

	require.Truef(t, sawCellmodulesFile,
		"%s scope regression: no production cellmodules/ file was scanned (tree renamed?). "+
			"A vacuous green here would hide every future violation.",
		ruleWithManagedResourceCellmoduleFunnel01)

	assert.Emptyf(t, violations,
		"%s: bootstrap.WithManagedResource referenced inside cellmodules/ (offending sites: %v). "+
			"Cell modules must declare resources via composition.ModuleResult.Resources; the "+
			"Builder is the sole sanctioned site that calls WithManagedResource (and derives "+
			"rollback from the same source). Move the resource into the returned "+
			"ModuleResult.Resources slice.",
		ruleWithManagedResourceCellmoduleFunnel01, violations)
}

// TestWithManagedResourceCellmoduleFunnel01_Fixtures is the reverse self-check
// mandated by charter §"工具选定后强制盲区自检": it proves the detector actually
// fires for every reference form (call / func-value / import-alias / dot-import)
// and does NOT over-fire on a same-named func from another package.
//
// Each fixture pattern is non-recursive (Run(t, Typed(...))) so a typed Run yields
// exactly the fixture package as a Pass (deps loaded for type info but not yielded).
// Typed (rather than StandaloneModule) is used because the RED fixtures import the
// main-module package runtime/bootstrap to reference the REAL WithManagedResource
// *types.Func — an isolated module would need a replace directive.
func TestWithManagedResourceCellmoduleFunnel01_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		// wantMin is the minimum violation count for RED dirs (4 reference forms:
		// call + func-value in call.go, alias in alias.go, dot-import in
		// dotimport.go). wantZero marks the GREEN dir (homonym, must not fire).
		wantMin  int
		wantZero bool
	}{
		{name: "red all reference forms", dir: "red", wantMin: 4},
		{name: "green homonym decoy", dir: "green", wantZero: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pattern := "./" + withManagedResourceFixturePrefix + tc.dir

			var diags []Diagnostic
			scanned := false
			_ = Run(t, Typed(TypedOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
				if len(p.Files) > 0 {
					scanned = true
				}
				diags = append(diags, collectWithManagedResourceFunnelViolations(p, withManagedResourceFixturePrefix)...)
				return nil
			})

			require.Truef(t, scanned, "fixture %s: no package loaded (path renamed?)", tc.dir)

			if tc.wantZero {
				assert.Emptyf(t, diags,
					"%s green fixture %s: a same-named func from another package must NOT resolve "+
						"to runtime/bootstrap.WithManagedResource (offending: %v)",
					ruleWithManagedResourceCellmoduleFunnel01, tc.dir, diags)
				return
			}
			assert.GreaterOrEqualf(t, len(diags), tc.wantMin,
				"%s red fixture %s: detector must flag every reference form "+
					"(call / func-value / import-alias / dot-import); got %d, want >= %d",
				ruleWithManagedResourceCellmoduleFunnel01, tc.dir, len(diags), tc.wantMin)
		})
	}
}
