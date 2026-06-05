// INVARIANT: CELL-INIT-CONTRACTUSAGE-01: kernel/cell must not import runtime/* or adapters/*; Registrar type must stay local
package archtest

// cell_init_test.go enforces structural invariants on the kernel/cell package:
//
//  1. kernel/cell must not import runtime/* or adapters/* (layer boundary).
//  2. The Registrar type must be defined in the kernel/cell package (locality).
//
// These guards prevent accidental re-introduction of deleted contributor
// interfaces or upward dependencies.
//
// Not registered in StandardCellRules: these rules reason about GoCell's own
// kernel/cell layout, which is vacuous for external Cell repos. Detector logic
// lives in cell_init.go (non-test) so the Check* functions are linkable from
// the dogfood tests here.
//
// # Blind-spot inventory and RED fixture coverage
//
// Both rules use go/types package-scope introspection via Run(t, Typed(…)) and
// share their core detector helpers (scanPassForForbiddenImports /
// scanPassForRegistrarLocality) with the RED fixture tests below. The RED
// tests drive the helpers against synthetic fixture packages to prove they are
// not vacuously green.
//
// Known residual blind-spots (go/types ceiling, not addressable without
// whole-program analysis):
//
//  1. A build-tag-gated import of runtime/* in kernel/cell would not be
//     detected unless the tag is listed in the scan's build tags. The default
//     scan (TypedOpts{Tests: false}) uses the default build config; a
//     runtime/ import behind `//go:build prod` would pass undetected. This is
//     accepted residual risk: kernel/ build-tag files are reviewed manually
//     and kernel/ has no prod-only tags today.
//
//  2. An alias (type KernelRegistrar = someotherpkg.Registrar) would show
//     scanPassForRegistrarLocality a *types.TypeName whose Pkg() is the
//     kernel/cell package, so the pkg-path check passes — but the underlying
//     type lives elsewhere. The current check does not dereference aliases.
//     Reverse self-check: TestKernelCell_RegistrarDefinedHere_RedAliasIsAccepted
//     documents this accepted blind-spot.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cellInitFixtureRoot is the testdata root for cell_init fixture packages.
const cellInitFixtureRoot = "./tools/archtest/testdata/cell_init_fixtures/"

// TestKernelCell_DoesNotImportRuntime confirms that no file in kernel/cell
// imports a package under runtime/* or adapters/*. This enforces the GoCell
// layering rule: kernel/ must not depend on runtime/ or adapters/.
func TestKernelCell_DoesNotImportRuntime(t *testing.T) {
	t.Parallel()
	diags := CheckKernelCellDoesNotImportRuntime(t, ConfigForExternalCell{})
	assert.Empty(t, diags,
		"kernel/cell must not import runtime/* or adapters/*; violations: %v", diags)
}

// TestKernelCell_DoesNotImportRuntime_RedFixture is the RED reverse self-check
// for CheckKernelCellDoesNotImportRuntime / scanPassForForbiddenImports:
// a fixture package that imports "runtime/debug" must trigger at least one
// diagnostic, proving the detector is not vacuously green.
//
// The fixture drives scanPassForForbiddenImports directly (bypassing the
// "./kernel/cell" hard-coded scope of the production Check* wrapper) so it
// compiles and loads only the small fixture package, not the full GoCell tree.
func TestKernelCell_DoesNotImportRuntime_RedFixture(t *testing.T) {
	t.Parallel()
	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{cellInitFixtureRoot + "red_imports_runtime"}),
		func(p *Pass) []Diagnostic {
			diags = append(diags, scanPassForForbiddenImports(p, "kernel/cell")...)
			return nil
		})
	require.NotEmpty(t, diags,
		"RED fixture red_imports_runtime: expected at least one diagnostic from "+
			"scanPassForForbiddenImports for a package importing runtime/debug, got none")
	require.Contains(t, diags[0].Message, "runtime/",
		"RED fixture: diagnostic must identify the runtime/ import, got %q", diags[0].Message)
}

// TestKernelCell_RegistrarDefinedHere confirms that the Registrar interface type
// is declared in kernel/cell (not aliased from another package). This prevents
// the interface from migrating out of the canonical layer boundary. The interface
// was renamed from Registry to Registrar in PR #615 (G-10) to match the
// Kratos-style noun/verb distinction (Registrar = verb interface; Registry would
// imply a storage/lookup noun).
func TestKernelCell_RegistrarDefinedHere(t *testing.T) {
	t.Parallel()
	diags := CheckKernelCellRegistrarDefinedHere(t, ConfigForExternalCell{})
	// The check returns the first failing condition; require the first assertion
	// (Registrar must be found) before asserting on the others.
	require.Empty(t, diags,
		"kernel/cell Registrar invariants failed: %v", diags)
}

// TestKernelCell_RegistrarDefinedHere_RedFixture is the RED reverse self-check
// for CheckKernelCellRegistrarDefinedHere / scanPassForRegistrarLocality:
// a fixture package that declares Registrar as a struct (not an interface) must
// trigger the "must be an interface type" diagnostic, proving the detector is
// not vacuously green.
//
// The fixture drives scanPassForRegistrarLocality directly so it compiles and
// loads only the small fixture package, not the full GoCell tree.
func TestKernelCell_RegistrarDefinedHere_RedFixture(t *testing.T) {
	t.Parallel()
	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{cellInitFixtureRoot + "red_registrar_not_iface"}),
		func(p *Pass) []Diagnostic {
			if p == nil || p.Pkg == nil {
				return nil
			}
			// Use the fixture package's own path as wantPkgPath so the
			// pkg-locality check passes and only the "not an interface" branch
			// is exercised. This isolates the interface-type assertion.
			diags = append(diags, scanPassForRegistrarLocality(p, "kernel/cell", p.Pkg.Path())...)
			return nil
		})
	require.NotEmpty(t, diags,
		"RED fixture red_registrar_not_iface: expected at least one diagnostic from "+
			"scanPassForRegistrarLocality for a Registrar declared as struct, got none")
	require.Contains(t, diags[0].Message, "interface",
		"RED fixture: diagnostic must mention 'interface', got %q", diags[0].Message)
}
