// INVARIANT: PROJECTION-APPLY-HOOK-FUNNEL-01
//
// PROJECTION-APPLY-HOOK-FUNNEL-01 — Coordinator.Subscribe caller allowlist.
//
// kernel/projection.Coordinator.Subscribe registers an Apply hook with the
// cell.Registrar (it calls reg.Subscribe internally). This is the per-projection
// wiring callsite; it must only appear in:
//
//   - kernel/projection/ itself (the method body calls reg.Subscribe internally)
//   - _test.go files (tests of the Coordinator API — explicitly allowed)
//   - the single sanctioned bootstrap projection drain file
//     runtime/bootstrap/phases_projection.go — the ONE place that constructs a
//     Coordinator from framework-owned deps and drives Coordinator.Subscribe.
//
// Relocation (PR-04a, Option A — ADR §7 amendment 2026-05-31): the sanctioned
// callsite moved from the cellgen-generated cell_gen.go to the bootstrap drain.
// Reason: Coordinator.Subscribe must be fed framework-owned raw infrastructure
// (CheckpointStore / TxRunner / Cursor / ReplaySource), which cell code may never
// hold (sealed-marker architecture). So cellgen emits the record-only
// reg.RegisterProjection (guarded by PROJECTION-REGISTER-FUNNEL-01) into
// cell_gen.go, and bootstrap — which legally holds the raw deps — constructs the
// Coordinator and calls Subscribe. The Subscribe terminal is now a single
// hand-written kernel/runtime file under tight review, a TIGHTER sanctioned set
// than "any cell_gen.go".
//
// Any other production callsite bypasses the drain funnel and constitutes a
// hand-rolled wiring that diverges from the single source of truth.
//
// PR-04a status: genuinely-green active guard (NOT t.Skip). The only production
// callsite is runtime/bootstrap/phases_projection.go; any other fires.
//
// # AI-robust grading (Funnel 双向锁评级)
//
//   - Downstream: Medium archtest caller allowlist. Go has no type-system
//     mechanism to restrict who calls a public method (Go visibility applies
//     to packages, not callers), so this is the strongest achievable form
//     for a method-call restriction. Archtest enforcement is CI fail-closed.
//     The sanctioned set is a single named file, tighter than the prior
//     "any cell_gen.go".
//   - Upstream: Medium — and permanently so (won't-do, gh #1372). A "cellgen-only
//     sealed token" that would make a hand-written Subscribe call uncompilable is
//     NOT expressible in Go: cellgen emits this call into the cell's OWN package
//     (cell_gen.go sits beside the hand-written cell.go), and Go has no
//     compile-time identity for "generated code". Any token constructor the
//     generated file can call, a hand-written sibling in the same package can call
//     too. Same permanent Go-language ceiling as Subscribe / RegisterWebhookReceiver
//     and the holder-seal family #851 / #893 / #1282.
//
// Because both sides are Medium and the upstream side cannot be Hard-ized, this is
// NOT a closed-loop Hard funnel and will not become one (gh #1372 closed won't-do).
// The raw-infra-stays-in-bootstrap property IS Hard (type system): cells
// hold sealed markers, never the CheckpointStore/TxRunner constructed in the
// drain — that is what makes Option A sound vs emitting NewCoordinator into
// generated cell code.
//
// # Blind spots (forms *types.Info / ResolveMethodCall cannot see)
//
//   - B1. Function-value / method-expression indirection:
//     `f := coord.Subscribe; f(ctx, spec, id, apply)` — the CallExpr's Fun is
//     an *ast.Ident resolving to a *types.Var, not a *types.Func, so
//     ResolveMethodCall returns (nil, false) and the call is invisible to R1.
//     Reverse self-check TestProjectionApplyHookFunnel01_ReverseBlindSpot_NoFuncValue
//     asserts no production code holds a Coordinator.Subscribe function value.
//
//   - B2. Wrapper method indirection: a wrapper struct that embeds *Coordinator
//     and calls Subscribe inside its own method body — the wrapper's callsite
//     IS caught (it's a direct method call on the embedded receiver), but the
//     wrapper struct itself could be in a non-allowlisted package. Covered by
//     the regular rule scan; not a blind spot.
//
//   - B3. Dot-import: `import . "…/projection"` — ResolveMethodCall handles dot-import
//     via *types.Info.Uses, so this form is caught. Not a blind spot.
//
// ref: tools/archtest/healthz_invariants_test.go (HEALTHZ-WRITE-01/A2 caller-allowlist pattern)
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3 PR-01
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	projectionCoordPkgPath    = "github.com/ghbvf/gocell/kernel/projection"
	projectionCoordTypeName   = "Coordinator"
	projectionSubscribeMethod = "Subscribe"
)

// isProjectionSubscribeCall reports whether call is a method call to
// (*kernel/projection.Coordinator).Subscribe resolved via *types.Info.Selections.
func isProjectionSubscribeCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != projectionSubscribeMethod {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != projectionCoordPkgPath {
		return false
	}
	if fn.Name() != projectionSubscribeMethod {
		return false
	}
	// Verify receiver is *Coordinator (not some other type named Subscribe).
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	recv := sig.Recv()
	if recv == nil {
		return false
	}
	recvT := recv.Type()
	if ptr, ok := recvT.(*types.Pointer); ok {
		recvT = ptr.Elem()
	}
	named, ok := recvT.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == projectionCoordPkgPath &&
		named.Obj().Name() == projectionCoordTypeName
}

// projectionDrainFile is the single sanctioned production Coordinator.Subscribe
// callsite (PR-04a, Option A): the bootstrap projection drain. It constructs the
// Coordinator from framework-owned deps and drives Subscribe via a capture
// Registrar. No generated file calls Subscribe under Option A.
const projectionDrainFile = "runtime/bootstrap/phases_projection.go"

// isProjectionApplyHookAllowed reports whether the file at the given rel path is
// in the allowed set for Coordinator.Subscribe calls:
//   - kernel/projection package itself (where the method is defined and used internally)
//   - _test.go files (tests of the Coordinator API)
//   - the single bootstrap projection drain file (runtime/bootstrap/phases_projection.go)
//
// Generated files (cell_gen.go / healthz_gen.go / slice_gen.go) are NOT in the
// set: under Option A cellgen emits reg.RegisterProjection (record-only), not
// Coordinator.Subscribe. The cell_gen.go RegisterProjection callsite is guarded
// separately by PROJECTION-REGISTER-FUNNEL-01.
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

func isProjectionApplyHookAllowed(rel, _ string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	if strings.HasPrefix(rel, "kernel/projection/") {
		return true
	}
	if filepath.ToSlash(rel) == projectionDrainFile {
		return true
	}
	return false
}

// TestProjectionApplyHookFunnel01 enforces PROJECTION-APPLY-HOOK-FUNNEL-01:
// Coordinator.Subscribe must only be called from the kernel/projection package
// itself, from _test.go files, or from cellgen-generated files. Any other
// production callsite is a violation.
//
// PR-01 status: genuinely-green (zero external callsites). The rule actively
// guards against hand-written callsites before PR-04 cellgen lands.
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

	root := findModuleRoot(t)
	allPatterns := prodscan.Patterns(root)

	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				if isProjectionApplyHookAllowed(rel, absPath) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isProjectionSubscribeCall(call, p.TypesInfo) {
						return
					}
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: fmt.Sprintf(
							"PROJECTION-APPLY-HOOK-FUNNEL-01: Coordinator.Subscribe called "+
								"from non-allowlisted file %s (line %d). "+
								"Allowed callers: kernel/projection/ itself, _test.go files, "+
								"and the single bootstrap projection drain "+
								"(runtime/bootstrap/phases_projection.go). "+
								"Declare projections via reg.RegisterProjection in cell_gen.go "+
								"(generated by cellgen from slice.yaml contractUsages); "+
								"bootstrap constructs the Coordinator and calls Subscribe automatically.",
							rel, p.Fset.Position(call.Pos()).Line,
						),
					})
				})
			}
			return nil
		})

	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Rel != diags[j].Rel {
			return diags[i].Rel < diags[j].Rel
		}
		return diags[i].Line < diags[j].Line
	})
	Report(t, "PROJECTION-APPLY-HOOK-FUNNEL-01", diags)
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
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				if isProjectionApplyHookAllowed(rel, absPath) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isProjectionSubscribeCall(call, p.TypesInfo) {
						return
					}
					diags = append(diags, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(call.Pos()).Line,
						Message: "PROJECTION-APPLY-HOOK-FUNNEL-01: rogue Coordinator.Subscribe callsite",
					})
				})
			}
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
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				if isProjectionApplyHookAllowed(rel, absPath) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isProjectionSubscribeCall(call, p.TypesInfo) {
						return
					}
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
					})
				})
			}
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
