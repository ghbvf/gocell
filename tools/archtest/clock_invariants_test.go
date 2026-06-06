// invariants:
//   - INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//   - INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//   - INVARIANT: PROD-CLOCK-INJECTION-01 (control-plane host: kernel/reconcile/,
//     which realizes RECONCILE-LOOP-CLOCK-CARVEOUT-01)
//   - INVARIANT: CLOCK-POSITIONAL-INJECTION-01
//
// Package archtest — clock injection invariants.
//
// Dogfoods clock_invariants.go Check* functions, which are the importable
// non-test home for rule logic (see that file's package godoc for rationale).
//
// Note: prod_clock_injection_fixtures_test.go is a companion fixture file and
// is kept separate (not merged).
package archtest

import (
	"path/filepath"
	"testing"
)

// INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//
// TestKernelClockLeafFallback enforces KERNEL-CLOCK-LEAF-FALLBACK-01:
// leaf-level clock.Real() construction is forbidden outside the composition root.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6 closure
func TestKernelClockLeafFallback(t *testing.T) {
	t.Parallel()
	Report(t, "KERNEL-CLOCK-LEAF-FALLBACK-01", CheckKernelClockLeafFallback(t, ConfigForExternalCell{}))
}

// runLeafFallbackFixtureScan loads the fixture package at fixtureDir and
// returns the sorted slice of violation Diagnostics.
func runLeafFallbackFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, clockScanLeafRealCallsAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestKernelClockLeafFallbackFixtures runs the KERNEL-CLOCK-LEAF-FALLBACK-01
// scanner over each fixture subpackage.
// Each fixture dir owns a diag.golden; GREEN fixtures have an empty golden.
func TestKernelClockLeafFallbackFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_leaf_fallback_fixtures")

	dirs := []string{"compliant", "violates"}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got := runLeafFallbackFixtureScan(t, base+"/"+dir)
			AssertGolden(t, filepath.Join(base, dir, "diag.golden"), got)
		})
	}
}

// INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//
// TestKernelClockResetRelativeProd enforces KERNEL-CLOCK-RESET-RELATIVE-PROD-01:
// production code must use the absolute Timer.ResetAt(deadline time.Time) API.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
func TestKernelClockResetRelativeProd(t *testing.T) {
	t.Parallel()
	Report(t, "KERNEL-CLOCK-RESET-RELATIVE-PROD-01", CheckKernelClockResetRelativeProd(t, ConfigForExternalCell{}))
}

// runClockResetRelativeFixtureScan loads a standalone fixture module and runs
// the same scanner as TestKernelClockResetRelativeProd.
func runClockResetRelativeFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, clockScanResetRelativeAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestKernelClockResetRelativeFixtures verifies the scanner against fixture packages.
func TestKernelClockResetRelativeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_reset_relative_fixtures")

	dirs := []string{"compliant", "violates"}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got := runClockResetRelativeFixtureScan(t, base+"/"+dir)
			AssertGolden(t, filepath.Join(base, dir, "diag.golden"), got)
		})
	}
}

// INVARIANT: PROD-CLOCK-INJECTION-01
//
// TestProdClockInjection enforces PROD-CLOCK-INJECTION-01 against the
// production tree: no direct reference to stdlib time wall-clock entry points
// (time.Now, time.Since, time.Until, time.NewTimer, etc.) in production code.
// All wall-clock interactions must flow through an injected kernel/clock.Clock.
//
// Control-plane carve-out (host-scoped (host, method, callee) triple, #619 +
// #1275 F1): see clock_invariants.go and clockAllowedMethods for the triple
// enforcement logic.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
func TestProdClockInjection(t *testing.T) {
	t.Parallel()
	Report(t, "PROD-CLOCK-INJECTION-01", CheckProdClockInjection(t, ConfigForExternalCell{}))
}

// INVARIANT: CLOCK-POSITIONAL-INJECTION-01
//
// TestClockPositionalInjection enforces CLOCK-POSITIONAL-INJECTION-01:
//   - Sub-check A: clock.MustHaveClock arg0 must be a positional parameter.
//   - Sub-check B: exported clock option-injector free functions are banned.
//   - Sub-check C: exported Config/Options/Opts struct fields of type clock.Clock
//     are banned (#1136 review F1).
//
// AI-robust grading:
//
//   - Downstream Hard: this archtest locks the form (any MustHaveClock call
//     with a non-param arg0 is a violation; any exported clock-option-injector
//     function is a violation).
//
//   - Upstream Hard: the Go compiler is the upstream enforcement. A mandatory
//     positional `clk clock.Clock` parameter makes omission a compile error.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D
func TestClockPositionalInjection(t *testing.T) {
	t.Parallel()
	Report(t, "CLOCK-POSITIONAL-INJECTION-01", CheckClockPositionalInjection(t, ConfigForExternalCell{}))
}

// runClockPositionalInjectionFixtureScan loads the fixture package at fixtureDir
// and returns the sorted slice of violation Diagnostics using the same predicate
// as TestClockPositionalInjection.
func runClockPositionalInjectionFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanClockPositionalInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestClockPositionalInjectionFixtures runs CLOCK-POSITIONAL-INJECTION-01
// over each fixture subpackage and asserts against the golden.
// GREEN fixtures have an empty golden; RED fixtures capture the expected output.
func TestClockPositionalInjectionFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_positional_injection_fixtures")

	// GREEN: compliant (arg0 is a param; no clock option-injector).
	// RED: violations (selector arg0; exported With*Clock option-injector).
	dirs := []string{
		"param_passes",                     // GREEN: MustHaveClock(clk, ...) where clk is a param
		"selector_violates",                // RED: MustHaveClock(cfg.Clock, ...) — selector arg0
		"withclock_violates",               // RED: exported func WithClock(...) — exact name
		"withfooclock_violates",            // RED: exported func WithFooClock(...) — suffixed name (broadened predicate)
		"aliased_import_selector_violates", // RED: aliased import + selector arg0
		"ctx_param_passes",                 // GREEN: (ctx, clk) two-param form — clk is a param
		"struct_field_violates",            // RED: exported Config-suffix struct with exported Clock field (sub-check C)
		"struct_field_unexported_ok",       // GREEN: unexported struct + unexported field; non-Config suffix
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(base, dir)
			diags := runClockPositionalInjectionFixtureScan(t, fixtureDir)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
