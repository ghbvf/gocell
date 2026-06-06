// Importable rule body for PROJECTION-APPLY-HOOK-FUNNEL-01. Migrated from the
// legacy _test.go form to a non-test .go (M3 #1302 / issue #1635) so external
// Cell repos can import and run it via StandardCellRules / RunStandardCellRules.
// The dogfood + RED/blind-spot reverse fixtures live in the non-test companion
// projection_apply_hook_funnel_test.go.
//
// # PROJECTION-APPLY-HOOK-FUNNEL-01
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
// # External Cell repo semantics (this rule is registered in StandardCellRules)
//
// The sanctioned bootstrap drain (runtime/bootstrap/phases_projection.go) lives
// in the GoCell platform module, NOT in a consumer repo. The production-file
// exemptions are bound to platform package identity (isProjectionApplyHookAllowed
// → isGoCellPlatformPkgPath), so a consumer module CANNOT claim them by forging
// the kernel/projection/… or drain rel path — its package path is not under
// PlatformModulePath. The rule therefore degrades to a PURE BAN for external Cell
// repos: any Coordinator.Subscribe call in consumer code fires immediately. That
// is the intended semantics — consumers must declare projections via
// reg.RegisterProjection (cellgen-generated from slice.yaml) and never wire the
// Coordinator directly. A clean external repo has zero Coordinator.Subscribe
// calls (vacuous-green).
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
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleProjectionApplyHookFunnel01 = "PROJECTION-APPLY-HOOK-FUNNEL-01"

const (
	// projectionCoordPkgPath is the import path of kernel/projection, derived from
	// PlatformModulePath so a module rename / /v2 bump updates one place.
	projectionCoordPkgPath    = PlatformModulePath + "/kernel/projection"
	projectionCoordTypeName   = "Coordinator"
	projectionSubscribeMethod = "Subscribe"
)

// projectionDrainFile is the single sanctioned production Coordinator.Subscribe
// callsite (PR-04a, Option A): the bootstrap projection drain. It constructs the
// Coordinator from framework-owned deps and drives Subscribe via a capture
// Registrar. No generated file calls Subscribe under Option A.
const projectionDrainFile = "runtime/bootstrap/phases_projection.go"

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

// isProjectionApplyHookAllowed reports whether a Coordinator.Subscribe call in the
// package pkgPath / file rel is in the allowed set:
//   - _test.go files (tests of the Coordinator API) — exempt regardless of package
//   - kernel/projection package itself (where the method is defined and used internally)
//   - the single bootstrap projection drain file (runtime/bootstrap/phases_projection.go)
//
// The two production-file exemptions are bound to PLATFORM PACKAGE IDENTITY
// (isGoCellPlatformPkgPath): a repo-relative path alone is not a trustworthy
// identity in a consumer module, which could recreate kernel/projection/… or the
// drain rel path to claim the GoCell-internal exemption and defeat the registered
// rule's pure ban. A forged site in a consumer module has a non-platform package
// path and is correctly NOT exempt — so this returns false for all non-test
// consumer files and the rule stays a pure ban there (intended).
//
// Generated files (cell_gen.go / healthz_gen.go / slice_gen.go) are NOT in the
// set: under Option A cellgen emits reg.RegisterProjection (record-only), not
// Coordinator.Subscribe. The cell_gen.go RegisterProjection callsite is guarded
// separately by PROJECTION-REGISTER-FUNNEL-01.
func isProjectionApplyHookAllowed(pkgPath, rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	if !isGoCellPlatformPkgPath(pkgPath) {
		return false
	}
	if strings.HasPrefix(rel, "kernel/projection/") {
		return true
	}
	return filepath.ToSlash(rel) == projectionDrainFile
}

// collectProjectionApplyHookViolations is the single per-Pass scanner shared by
// CheckProjectionApplyHookFunnel01 and the reverse fixtures (no parallel rule
// body). It walks each non-allowed non-test file for Coordinator.Subscribe calls.
func collectProjectionApplyHookViolations(p *Pass) []Diagnostic {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	var out []Diagnostic
	pkgPath := p.Pkg.Path()
	for _, f := range p.Files {
		rel := p.Rel(f)
		if isProjectionApplyHookAllowed(pkgPath, rel) {
			continue
		}
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			if !isProjectionSubscribeCall(call, p.TypesInfo) {
				return
			}
			out = append(out, Diagnostic{
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
	return out
}

// CheckProjectionApplyHookFunnel01 enforces PROJECTION-APPLY-HOOK-FUNNEL-01
// downstream: Coordinator.Subscribe in any production package may be called only
// from kernel/projection/ itself, _test.go files, or the single bootstrap
// projection drain (runtime/bootstrap/phases_projection.go).
//
// It scans the running module's production code (Production → findModuleRoot),
// covering tag-gated files via cfg.BuildTags, and returns the diagnostics it
// observes.
//
// External Cell repo semantics (this rule is registered in StandardCellRules):
// the sanctioned drain and kernel/projection/ live in GoCell's platform module,
// not in a consumer module — the rule degrades to a pure ban there. A clean
// external repo with no Coordinator.Subscribe calls is vacuous-green.
func CheckProjectionApplyHookFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	// Scan twice per the ConfigForExternalCell.BuildTags contract: the default
	// build config first (so files behind //go:build !<tag> are not missed), then
	// the tagged config when cfg.BuildTags is non-empty (so files behind
	// //go:build <tag> are covered). A tagged-only load EXCLUDES default-only
	// files, so a single tagged pass would leave a hole — same default+tagged
	// shape as CheckPanicRegistered / CheckScaffoldDerivedForceOverwrite.
	// scanner.Canonical dedups the overlap (an unconstrained file is loaded by
	// both passes).
	out := Run(t, Production(TypedOpts{}), collectProjectionApplyHookViolations)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), collectProjectionApplyHookViolations)...)
	}
	return scanner.Canonical(out)
}
