// INVARIANT: RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01
//
// AI-robust: Medium (test-infra funnel; Hard unreachable — permanent ceiling).
//
// Funnels all goroutine-leak verification in the kernel/reconcile test binaries
// through a single package-level goleak.VerifyTestMain, banning the per-test
// goleak.VerifyNone(IgnoreCurrent()) idiom that flakes under -race (gh #1568).
//
// # Why the per-test idiom is unsafe here (and VerifyTestMain is not)
//
// goleak.IgnoreCurrent() snapshots the goroutine set at defer-arg evaluation
// (test start). Loop.Stop() deterministically joins everything in its WaitGroup
// (workers / feeder / waitingLoop / leaderManage), but by design does NOT join
// (a) the Trigger goroutine (fire-and-forget, ctx-bound — mirrors
// controller-runtime's Channel source `go cs.syncLoop(ctx)`, never joined) nor
// (b) watchDrain's tail (it close(done)s — unblocking Stop — then returns).
// Both drain asynchronously on ctx cancel. A per-test VerifyNone races those
// drains against goleak's retry budget; under -race + full-package CPU
// contention the budget is exceeded and a not-yet-drained goroutine is
// misattributed to whichever test's IgnoreCurrent snapshot missed it. A single
// package-level VerifyTestMain runs once after every test, so the async drain
// settles well within goleak's retry budget. The fire-and-forget Trigger
// contract is intentional and correct, so the fix is test-side, not in loop.go.
//
// # Funnel double-lock (both prongs Medium; Hard unreachable)
//
//   - A1 downstream ban: production-grade go/types resolution (ResolvePackageRef,
//     import-alias-proof) flags any goleak.VerifyNone call in a _test.go of the
//     in-scope dirs. Re-introducing the flaky idiom is CI-red, not a silent
//     flake return.
//   - A2 upstream guard: each in-scope package MUST declare a TestMain whose body
//     calls goleak.VerifyTestMain. Deleting the package-level guard (which would
//     silently drop all leak coverage) is CI-red.
//
// Hard is UNREACHABLE for both prongs: "a test must call VerifyTestMain" and "a
// test must not call VerifyNone" are test conventions — Go cannot require a
// TestMain to exist nor forbid a function call at compile time. This differs in
// KIND from the holder-seal ceilings (SPAN-SETATTR-HOLDER-SEAL #851 /
// HEALTHZ-HOLDER-SEAL #893), which have a *conceivable* type-system Hard form
// (seal an interface) that is merely infeasible; here no Hard form is even
// conceivable, as for all goleak usage. The Hard-ization assessment → won't-do
// is recorded at gh #1607 (matching the reconcile-Medium-archtest convention of
// citing a gh issue, cf. #1416 / #1418).
//
// # Scope (the package that exhibited the flake — #1568)
//
// In scope: kernel/reconcile + kernel/reconcile/reconciletest. Both run Loops
// whose fire-and-forget Trigger makes the per-test IgnoreCurrent snapshot
// inherently fragile under -race. Scope is deliberately narrow — other packages
// use per-test goleak.VerifyNone without a reported flake, so a repo-wide ban
// would be over-reach (not an assertion that those packages are leak-join-
// complete — only that none has exhibited this flake). If another package with
// a fire-and-forget / ctx-bound non-joined goroutine later flakes the same way,
// extend reconcileGoleakDirs rather than widening to all packages.
//
// # Blind-spot catalog (charter §盲区反向自检)
//
//   - B1. import alias (`import gl "go.uber.org/goleak"; gl.VerifyNone(...)`):
//     handled — ResolvePackageRef resolves the callee's *types.Object identity,
//     not the source selector text, so an alias resolves to the same pkgPath.
//   - B2. bare goleak.IgnoreCurrent() without VerifyNone: inert (does nothing on
//     its own), deliberately NOT flagged — only VerifyNone is the per-test
//     verification entry point. This keeps VerifyTestMain options (e.g.
//     goleak.IgnoreTopFunction passed to VerifyTestMain) legal.
//   - B3. a goleak call laundered through a function value / wrapper helper:
//     ResolvePackageRef matches direct call selectors only; an indirected
//     goleak.VerifyNone is a known residual (same ceiling as every go/types
//     callsite scan). No production reconcile test uses indirection; the
//     RED fixture proves the direct-form branch fires.
//   - B4. external test package (package reconciletest_test): A1/A2 match by the
//     file's module-relative dir (path.Dir(rel)), NOT the package-declaration
//     name, and packages.Load(Tests: true) surfaces xtest files at their real
//     source paths — so main_test.go in the reconciletest_test xtest package is
//     correctly attributed to dir kernel/reconcile/reconciletest.
//   - B5. detector is filename-agnostic: goleakVerifyNoneViolations walks
//     CallExprs and resolves the callee's type; it does not depend on the
//     _test.go suffix. The production scan applies the _test.go + in-scope-dir
//     filter BEFORE calling it; the RED fixture (a build-tagged .go, not
//     _test.go) calls it directly, so the non-vacuity proof exercises the
//     identical detector branch.
//
// Non-vacuity: TestReconcileGoleakTestmainFunnel asserts the scan visited >0
// in-scope test files AND that A2 found a VerifyTestMain TestMain in every
// in-scope dir (proves the scan + the goleak resolver are live).
// TestReconcileGoleakTestmainFunnel_REDFixture proves the A1 detector fires on
// the banned VerifyNone and not on VerifyTestMain over a real buildable fixture.
//
// ref: tools/archtest/cell_repo_readyz_probe_test.go (test-file scan + RED model)
// ref: kubernetes-sigs/controller-runtime pkg/source/source.go (fire-and-forget source)
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	goleakPkgPath        = "go.uber.org/goleak"
	goleakVerifyNoneFunc = "VerifyNone"
	goleakVerifyTestMain = "VerifyTestMain"
)

// reconcileGoleakDirs are the module-relative package directories whose test
// binaries must funnel all goroutine-leak verification through a single
// package-level goleak.VerifyTestMain (gh #1568).
var reconcileGoleakDirs = []string{
	"kernel/reconcile",
	"kernel/reconcile/reconciletest",
}

// TestReconcileGoleakTestmainFunnel enforces RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01:
// A1 bans per-test goleak.VerifyNone in the in-scope dirs' _test.go files; A2
// requires each in-scope package to keep a TestMain calling goleak.VerifyTestMain.
func TestReconcileGoleakTestmainFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	inScope := make(map[string]bool, len(reconcileGoleakDirs))
	for _, d := range reconcileGoleakDirs {
		inScope[d] = true
	}

	var banDiags []Diagnostic
	verifyTestMainDirs := make(map[string]bool) // dir → declares TestMain → VerifyTestMain
	scanned := 0                                // in-scope _test.go files visited (vacuity guard)

	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") || !inScope[path.Dir(rel)] {
					continue
				}
				scanned++
				// A1: per-test VerifyNone ban.
				banDiags = append(banDiags, goleakVerifyNoneViolations(f, p.TypesInfo, rel, p.Fset)...)
				// A2: record dirs that keep the package-level VerifyTestMain guard.
				if fileHasVerifyTestMain(f, p.TypesInfo) {
					verifyTestMainDirs[path.Dir(rel)] = true
				}
			}
			return nil
		})

	// Vacuity guard: a scope/path regression that visits zero in-scope test
	// files would otherwise false-green both prongs.
	require.Positive(t, scanned,
		"RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01 vacuity: scan visited zero in-scope reconcile "+
			"test files %v — packages.Load scope or path-filter regression", reconcileGoleakDirs)

	// A1 downstream ban.
	Report(t, "RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01", banDiags)
	// A2 upstream guard (also non-vacuity: both dirs must be found).
	Report(t, "RECONCILE-GOLEAK-TESTMAIN-FUNNEL-01", missingVerifyTestMainDirs(reconcileGoleakDirs, verifyTestMainDirs))
}

// TestReconcileGoleakTestmainFunnel_REDFixture proves the A1 detector
// (goleakVerifyNoneViolations) fires on the banned per-test
// goleak.VerifyNone(IgnoreCurrent()) and does NOT fire on the sanctioned
// package-level goleak.VerifyTestMain — exercising the go/types resolution
// branch over a real buildable fixture (charter §盲区反向自检).
func TestReconcileGoleakTestmainFunnel_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/reconcilegoleakredfixture/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				diags = append(diags, goleakVerifyNoneViolations(f, p.TypesInfo, p.Rel(f), p.Fset)...)
			}
			return nil
		})

	// Exactly one flag: redPerTestVerifyNone's goleak.VerifyNone. The fixture's
	// greenVerifyTestMain (goleak.VerifyTestMain) and the nested goleak.IgnoreCurrent
	// must NOT be flagged — proving the detector matches only VerifyNone.
	require.Len(t, diags, 1,
		"A1 detector must flag exactly the RED goleak.VerifyNone (not VerifyTestMain, not "+
			"bare IgnoreCurrent); got %d: %v", len(diags), diags)
}

// TestReconcileGoleakTestmainFunnel_MissingDirsRED exercises the pure A2 flag
// logic (missingVerifyTestMainDirs) with synthetic found-sets — no packages.Load.
func TestReconcileGoleakTestmainFunnel_MissingDirsRED(t *testing.T) {
	t.Parallel()
	required := []string{"kernel/reconcile", "kernel/reconcile/reconciletest"}

	flagged := func(diags []Diagnostic) map[string]bool {
		out := make(map[string]bool, len(diags))
		for _, d := range diags {
			out[d.Rel] = true
		}
		return out
	}

	t.Run("all-present-green", func(t *testing.T) {
		t.Parallel()
		diags := missingVerifyTestMainDirs(required, map[string]bool{
			"kernel/reconcile": true, "kernel/reconcile/reconciletest": true,
		})
		assert.Empty(t, diags, "both guards present → zero violations")
	})

	t.Run("one-missing", func(t *testing.T) {
		t.Parallel()
		diags := missingVerifyTestMainDirs(required, map[string]bool{"kernel/reconcile": true})
		got := flagged(diags)
		assert.True(t, got["kernel/reconcile/reconciletest"], "missing reconciletest guard must be flagged")
		assert.False(t, got["kernel/reconcile"], "present reconcile guard must not be flagged")
		assert.Len(t, diags, 1, "exactly the missing dir flagged: %v", diags)
	})

	t.Run("none-present", func(t *testing.T) {
		t.Parallel()
		diags := missingVerifyTestMainDirs(required, map[string]bool{})
		assert.Len(t, diags, 2, "both missing guards flagged: %v", diags)
	})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// goleakVerifyNoneViolations returns a Diagnostic for every CallExpr in file
// whose callee resolves (via go/types, import-alias-proof) to
// go.uber.org/goleak.VerifyNone — the per-test leak-check idiom banned in the
// reconcile test binaries (gh #1568). VerifyTestMain (+ its options) and a bare
// goleak.IgnoreCurrent (inert without VerifyNone) are deliberately NOT flagged.
func goleakVerifyNoneViolations(file *ast.File, info *types.Info, rel string, fset *token.FileSet) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != goleakPkgPath || name != goleakVerifyNoneFunc {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "per-test goleak.VerifyNone is banned in the reconcile test binaries; " +
				"goroutine-leak verification must funnel through the package-level " +
				"goleak.VerifyTestMain in main_test.go. The Loop's fire-and-forget Trigger " +
				"goroutine + watchDrain tail drain asynchronously on ctx cancel, so a per-test " +
				"goleak.IgnoreCurrent() snapshot under -race misattributes a not-yet-drained " +
				"goroutine to the wrong test (gh #1568).",
		})
	})
	return diags
}

// fileHasVerifyTestMain reports whether file declares a top-level func TestMain
// whose body calls go.uber.org/goleak.VerifyTestMain.
func fileHasVerifyTestMain(file *ast.File, info *types.Info) bool {
	found := false
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if found || fd.Recv != nil || fd.Name == nil || fd.Name.Name != "TestMain" || fd.Body == nil {
			return
		}
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if pkgPath, name, ok := ResolvePackageRef(info, call.Fun); ok &&
				pkgPath == goleakPkgPath && name == goleakVerifyTestMain {
				found = true
			}
		})
	})
	return found
}

// missingVerifyTestMainDirs returns a sorted Diagnostic for every required dir
// absent from found. Pure function so the RED self-test can exercise the flag
// logic without packages.Load.
func missingVerifyTestMainDirs(required []string, found map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for _, dir := range required {
		if found[dir] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  dir,
			Line: 0,
			Message: "package " + dir + " must declare a TestMain calling goleak.VerifyTestMain " +
				"(e.g. main_test.go) — the package-level leak-verification funnel that replaces " +
				"per-test goleak.VerifyNone(IgnoreCurrent()) (gh #1568).",
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}
