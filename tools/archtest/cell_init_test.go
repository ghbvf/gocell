// INVARIANT: CELL-INIT-CONTRACTUSAGE-01: kernel/cell must not import runtime/* or adapters/*; Registry type must stay local
package archtest

// cell_init_test.go enforces structural invariants on the kernel/cell package:
//
//  1. kernel/cell must not import runtime/* or adapters/* (layer boundary).
//  2. The Registry type must be defined in the kernel/cell package (locality).
//
// These guards prevent accidental re-introduction of deleted contributor
// interfaces or upward dependencies.

import (
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const kernelCellPattern = "github.com/ghbvf/gocell/kernel/cell"

// TestKernelCell_DoesNotImportRuntime confirms that no file in kernel/cell
// imports a package under runtime/* or adapters/*. This enforces the GoCell
// layering rule: kernel/ must not depend on runtime/ or adapters/.
func TestKernelCell_DoesNotImportRuntime(t *testing.T) {
	root := findModuleRoot(t)
	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, "./kernel/cell")
	require.NoError(t, err, "packages.Load kernel/cell")
	require.Empty(t, errs, "kernel/cell load errors: %v", errs)
	require.Len(t, pkgs, 1, "expected exactly one kernel/cell package")

	pkg := pkgs[0]
	var violations []string
	for _, imp := range pkg.Imports {
		path := imp.PkgPath
		if strings.Contains(path, "runtime/") || strings.Contains(path, "adapters/") {
			violations = append(violations, path)
		}
	}

	assert.Empty(t, violations,
		"kernel/cell must not import runtime/* or adapters/*; found: %v", violations)
}

// TestKernelCell_RegistryDefinedHere confirms that the Registry interface type
// is declared in kernel/cell (not aliased from another package). This prevents
// the interface from migrating out of the canonical layer boundary.
func TestKernelCell_RegistryDefinedHere(t *testing.T) {
	root := findModuleRoot(t)
	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, "./kernel/cell")
	require.NoError(t, err, "packages.Load kernel/cell")
	require.Empty(t, errs, "kernel/cell load errors: %v", errs)
	require.Len(t, pkgs, 1, "expected exactly one kernel/cell package")

	pkg := pkgs[0]
	scope := pkg.Types.Scope()

	obj := scope.Lookup("Registry")
	require.NotNil(t, obj, "Registry must be defined in kernel/cell")

	tn, ok := obj.(*types.TypeName)
	require.True(t, ok, "Registry must be a type name")

	// Must be an interface declared in this package (not a type alias pointing elsewhere).
	named, ok := tn.Type().(*types.Named)
	require.True(t, ok, "Registry must be a named type")

	_, isIface := named.Underlying().(*types.Interface)
	assert.True(t, isIface, "Registry must be an interface type")

	// The package path of the type's object must be kernel/cell.
	assert.Equal(t, kernelCellPattern, obj.Pkg().Path(),
		"Registry must be defined in %s", kernelCellPattern)
}
