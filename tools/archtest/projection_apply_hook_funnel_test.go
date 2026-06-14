//go:build archtest

// INVARIANT: PROJECTION-APPLY-HOOK-FUNNEL-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckProjectionApplyHookFunnel01 + collectProjectionApplyHookViolations
// + isProjectionSubscribeCall + isProjectionApplyHookAllowed + the full package
// godoc) lives in the non-test companion projection_apply_hook_funnel.go, so
// external Cell repos can import and run it via StandardCellRules /
// RunStandardCellRules (M3 #1302 / issue #1635).
package archtest

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// TestProjectionApplyHookFunnel01_DrainFileExists guards against silent drift:
// if runtime/bootstrap/phases_projection.go is renamed/moved without updating
// projectionDrainFile, the allowlist would stop matching the real Subscribe
// callsite and the rule would flip to firing on legitimate code (or, if the
// file vanished, become vacuously green). Asserting the path exists keeps the
// Soft string anchor honest.
func TestProjectionApplyHookFunnel01_DrainFileExists(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(projectionDrainFile)))
	require.NoError(t, err,
		"projectionDrainFile %q must exist; update the const if the bootstrap drain moved", projectionDrainFile)
}

// TestIsProjectionApplyHookAllowed proves the production-file exemptions are bound
// to PLATFORM package identity, not just a repo-relative path. The critical cases
// are "consumer module forges kernel/projection/… or drain rel path": a consumer
// recreating either must STILL be flagged (false), because its package path is not
// under PlatformModulePath — closing the registered rule's pure-ban bypass (codex
// #1682 F3). The _test.go exemption is package-independent by design.
func TestIsProjectionApplyHookAllowed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		pkgPath, rel string
		want         bool
	}{
		{"_test.go exempt regardless of pkg", "consumer.example/app/foo", "foo/bar_test.go", true},
		{"sanctioned kernel/projection", PlatformFrameworkModulePath + "/kernel/projection", "kernel/projection/coordinator.go", true},
		{"sanctioned bootstrap drain", PlatformFrameworkModulePath + "/runtime/bootstrap", "runtime/bootstrap/phases_projection.go", true},
		{"consumer forges kernel/projection rel", "consumer.example/app/kernel/projection", "kernel/projection/coordinator.go", false},
		{"consumer forges drain rel", "consumer.example/app/runtime/bootstrap", "runtime/bootstrap/phases_projection.go", false},
		{"platform pkg, non-allowlisted rel", PlatformModulePath + "/cells/foo", "cells/foo/cell.go", false},
		{"unresolved pkg, drain rel", "", "runtime/bootstrap/phases_projection.go", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isProjectionApplyHookAllowed(tc.pkgPath, tc.rel); got != tc.want {
				t.Errorf("isProjectionApplyHookAllowed(%q, %q) = %v, want %v",
					tc.pkgPath, tc.rel, got, tc.want)
			}
		})
	}
}

// TestProjectionApplyHookFunnel01 dogfoods PROJECTION-APPLY-HOOK-FUNNEL-01
// against GoCell itself by calling the same CheckProjectionApplyHookFunnel01
// that StandardCellRules (and external Cell repos via RunStandardCellRules) use
// — single source, no parallel rule body.
//
// PR-04a status: genuinely-green active guard. The only production callsite is
// runtime/bootstrap/phases_projection.go; any other fires. (Under Option A no
// cellgen-generated file is a sanctioned Subscribe callsite — generated cell_gen.go
// emits the record-only reg.RegisterProjection instead, guarded by
// PROJECTION-REGISTER-FUNNEL-01.)
//
// # Blind spots (forms ResolveMethodCall cannot see)
//
//   - B1. Function-value form (`f := coord.Subscribe; f(...)`): Fun resolves
//     to *types.Var, not *types.Func — isProjectionSubscribeCall returns false.
//     Covered by TestProjectionApplyHookFunnel01_ReverseBlindSpot_NoFuncValue.
//
//   - B2. Wrapper/delegation: a wrapper type that embeds *Coordinator and calls
//     Subscribe inside its own exported method — the call IS caught by this rule
//     since it is a direct SelectorExpr call on the embedded receiver. Not a
//     blind spot; no production wrapper exists (confirmed vacuously).
func TestProjectionApplyHookFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, ruleProjectionApplyHookFunnel01,
		CheckProjectionApplyHookFunnel01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestProjectionApplyHookFunnel01_ReverseFixture loads the synthetic violation
// fixture and asserts the rule fires on the rogue callsite.
func TestProjectionApplyHookFunnel01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "projection_apply_hook_violate")

	var diags []Diagnostic

	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			diags = append(diags, collectProjectionApplyHookViolations(p)...)
			return nil
		})

	assert.NotEmpty(t, diags,
		"PROJECTION-APPLY-HOOK-FUNNEL-01 reverse fixture: expected ≥1 diagnostic for "+
			"rogue Coordinator.Subscribe call; rule logic is broken or fixture is missing the violation")
}

// TestProjectionApplyHookFunnel01_ReverseFixture_GeneratedNotSanctioned asserts
// that under Option A NO generated file is a sanctioned Coordinator.Subscribe
// callsite: a Subscribe call in cell_gen.go OR healthz_gen.go (both bearing the
// cellgen DO-NOT-EDIT banner) MUST fire. The only sanctioned production callsite
// is the bootstrap drain (runtime/bootstrap/phases_projection.go), which cannot
// appear in this fixture module — so every Subscribe here is a violation. This
// supersedes the pre-Option-A "cell_gen.go allowed" generated-scope distinction.
func TestProjectionApplyHookFunnel01_ReverseFixture_GeneratedNotSanctioned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "projection_apply_hook_violate")

	var diags []Diagnostic

	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			diags = append(diags, collectProjectionApplyHookViolations(p)...)
			return nil
		})

	var firedHealthz, firedCellGen bool
	for _, d := range diags {
		switch filepath.Base(d.Rel) {
		case "healthz_gen.go":
			firedHealthz = true
		case "cell_gen.go":
			firedCellGen = true
		}
	}
	assert.True(t, firedCellGen,
		"Option A: Coordinator.Subscribe in cell_gen.go MUST fire — generated files emit "+
			"reg.RegisterProjection (record-only), never Coordinator.Subscribe")
	assert.True(t, firedHealthz,
		"Option A: Coordinator.Subscribe in healthz_gen.go MUST fire — no generated file is "+
			"a sanctioned Subscribe callsite")
}

// TestProjectionApplyHookFunnel01_ReverseBlindSpot_NoFuncValue (blind spot B1)
// asserts no production non-test file holds a Coordinator.Subscribe function
// value (method expression `coord.Subscribe` assigned to a variable, not called).
// Such a value would escape R1's direct-CallExpr callee resolution.
func TestProjectionApplyHookFunnel01_ReverseBlindSpot_NoFuncValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.Patterns(root)

	var violations []string

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}

			calleePos := make(map[interface{}]struct{})
			for _, f := range p.Files {
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != projectionSubscribeMethod {
						return
					}
					fn, ok := ResolveMethodCall(p.TypesInfo, sel)
					if !ok || fn == nil {
						return
					}
					if fn.Pkg() == nil || fn.Pkg().Path() != projectionCoordPkgPath {
						return
					}
					calleePos[sel.Sel] = struct{}{}
				})
			}

			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if strings.HasPrefix(rel, "kernel/projection/") {
					continue
				}
				EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != projectionSubscribeMethod {
						return
					}

					if _, isCallee := calleePos[sel.Sel]; isCallee {
						return
					}
					fn, ok := ResolveMethodCall(p.TypesInfo, sel)
					if !ok || fn == nil {
						return
					}
					if fn.Pkg() == nil || fn.Pkg().Path() != projectionCoordPkgPath {
						return
					}
					violations = append(violations, fmt.Sprintf(
						"%s:%d: Coordinator.Subscribe used as function value (not a direct call) "+
							"— would escape PROJECTION-APPLY-HOOK-FUNNEL-01 detection (blind spot B1)",
						rel, p.Fset.Position(sel.Sel.Pos()).Line,
					))
				})
			}
			return nil
		})

	assert.Empty(t, violations,
		"blind spot B1: no production non-test file outside kernel/projection/ should hold "+
			"a Coordinator.Subscribe function value")
}
