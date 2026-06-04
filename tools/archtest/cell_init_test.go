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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKernelCell_DoesNotImportRuntime confirms that no file in kernel/cell
// imports a package under runtime/* or adapters/*. This enforces the GoCell
// layering rule: kernel/ must not depend on runtime/ or adapters/.
func TestKernelCell_DoesNotImportRuntime(t *testing.T) {
	t.Parallel()
	diags := CheckKernelCellDoesNotImportRuntime(t, ConfigForExternalCell{})
	assert.Empty(t, diags,
		"kernel/cell must not import runtime/* or adapters/*; violations: %v", diags)
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
