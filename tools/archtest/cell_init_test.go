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
)

const kernelCellPattern = "github.com/ghbvf/gocell/kernel/cell"

// TestKernelCell_DoesNotImportRuntime confirms that no file in kernel/cell
// imports a package under runtime/* or adapters/*. This enforces the GoCell
// layering rule: kernel/ must not depend on runtime/ or adapters/.
func TestKernelCell_DoesNotImportRuntime(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			path := imp.Path()
			if strings.Contains(path, "runtime/") || strings.Contains(path, "adapters/") {
				violations = append(violations, path)
			}
		}
		return nil
	})

	assert.Empty(t, violations,
		"kernel/cell must not import runtime/* or adapters/*; found: %v", violations)
}

// TestKernelCell_RegistryDefinedHere confirms that the Registry interface type
// is declared in kernel/cell (not aliased from another package). This prevents
// the interface from migrating out of the canonical layer boundary.
func TestKernelCell_RegistryDefinedHere(t *testing.T) {
	t.Parallel()

	var (
		found      bool
		isTypeName bool
		isIface    bool
		pkgPath    string
	)
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		scope := p.Pkg.Scope()
		obj := scope.Lookup("Registry")
		if obj == nil {
			return nil
		}
		found = true
		tn, ok := obj.(*types.TypeName)
		if !ok {
			return nil
		}
		isTypeName = true
		named, ok := tn.Type().(*types.Named)
		if !ok {
			return nil
		}
		_, ok = named.Underlying().(*types.Interface)
		isIface = ok
		if obj.Pkg() != nil {
			pkgPath = obj.Pkg().Path()
		}
		return nil
	})

	require.True(t, found, "Registry must be defined in kernel/cell")
	require.True(t, isTypeName, "Registry must be a type name")
	assert.True(t, isIface, "Registry must be an interface type")
	assert.Equal(t, kernelCellPattern, pkgPath,
		"Registry must be defined in %s", kernelCellPattern)
}
