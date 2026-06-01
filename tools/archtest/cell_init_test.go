// INVARIANT: CELL-INIT-CONTRACTUSAGE-01: kernel/cell must not import runtime/* or adapters/*; Registrar type must stay local
package archtest

// cell_init_test.go enforces structural invariants on the kernel/cell package:
//
//  1. kernel/cell must not import runtime/* or adapters/* (layer boundary).
//  2. The Registrar type must be defined in the kernel/cell package (locality).
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/cell"}), func(p *Pass) []Diagnostic {
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

// TestKernelCell_RegistrarDefinedHere confirms that the Registrar interface type
// is declared in kernel/cell (not aliased from another package). This prevents
// the interface from migrating out of the canonical layer boundary. The interface
// was renamed from Registry to Registrar in PR #615 (G-10) to match the
// Kratos-style noun/verb distinction (Registrar = verb interface; Registry would
// imply a storage/lookup noun).
func TestKernelCell_RegistrarDefinedHere(t *testing.T) {
	t.Parallel()

	var (
		found      bool
		isTypeName bool
		isIface    bool
		pkgPath    string
	)
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/cell"}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		scope := p.Pkg.Scope()
		obj := scope.Lookup("Registrar")
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

	require.True(t, found, "Registrar must be defined in kernel/cell")
	require.True(t, isTypeName, "Registrar must be a type name")
	assert.True(t, isIface, "Registrar must be an interface type")
	assert.Equal(t, kernelCellPattern, pkgPath,
		"Registrar must be defined in %s", kernelCellPattern)
}
