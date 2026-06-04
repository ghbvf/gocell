package archtest

// cell_init.go — non-test home of CELL-INIT-CONTRACTUSAGE-01 detector logic.
//
// This file is NOT registered in StandardCellRules: the rule reasons about
// GoCell's own kernel/cell package layout (verifying that kernel/cell does not
// import runtime/ or adapters/, and that the Registrar type lives in
// kernel/cell). External Cell repos do not have a kernel/cell package, so the
// rule is vacuous for external consumers.
//
// Platform symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here.

import (
	"go/types"
	"strings"
	"testing"
)

// cellInitKernelCellPkgPath is the canonical import path of the kernel/cell package.
// Anchored to PlatformModulePath — not a bare string literal.
const cellInitKernelCellPkgPath = PlatformModulePath + "/kernel/cell"

// CheckKernelCellDoesNotImportRuntime scans the kernel/cell package to confirm
// it imports neither runtime/* nor adapters/*. Returns diagnostics for each
// forbidden import found. Not registered in StandardCellRules (gocell-internal
// layout check, vacuous for external repos).
func CheckKernelCellDoesNotImportRuntime(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
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
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{
			Rel:     "kernel/cell",
			Line:    0,
			Message: "kernel/cell must not import runtime/* or adapters/*; found: " + v,
		})
	}
	return diags
}

// CheckKernelCellRegistrarDefinedHere confirms that the Registrar interface
// type is declared in kernel/cell (not aliased from another package). Not
// registered in StandardCellRules (gocell-internal layout check).
func CheckKernelCellRegistrarDefinedHere(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
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

	if !found {
		return []Diagnostic{{Rel: "kernel/cell", Line: 0, Message: "Registrar must be defined in kernel/cell"}}
	}
	if !isTypeName {
		return []Diagnostic{{Rel: "kernel/cell", Line: 0, Message: "Registrar must be a type name"}}
	}
	var diags []Diagnostic
	if !isIface {
		diags = append(diags, Diagnostic{Rel: "kernel/cell", Line: 0, Message: "Registrar must be an interface type"})
	}
	if pkgPath != cellInitKernelCellPkgPath {
		diags = append(diags, Diagnostic{
			Rel:     "kernel/cell",
			Line:    0,
			Message: "Registrar must be defined in " + cellInitKernelCellPkgPath + ", got " + pkgPath,
		})
	}
	return diags
}
