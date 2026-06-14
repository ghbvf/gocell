package archtest

// cell_init.go — non-test home of CELL-INIT-CONTRACTUSAGE-01 detector logic.
//
// This file is NOT registered in StandardCellRules: the rule reasons about
// GoCell's own kernel/cell package layout (verifying that kernel/cell does not
// import runtime/ or adapters/, and that the Registrar type lives in
// kernel/cell). External Cell repos do not have a kernel/cell package, so the
// rule is vacuous for external consumers — like the other gocell-internal cell
// rules (cell_init_checknotnoop.go, cell_public_option_param.go,
// cell_repo_readyz_probe.go, cell_test_no_adapter_import.go), none of which
// ship in StandardCellRules().
//
// Platform symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here.
//
// The exported Check* wrappers and their scan helpers
// (scanPassForForbiddenImports / scanPassForRegistrarLocality) live in this
// non-test file so both the dogfood tests (cell_init_test.go) and the RED
// fixture self-checks call the identical detector — single source, no parallel
// rule body. They are in a non-test file only for that in-repo linkability; the
// rule is still gocell-internal and intentionally absent from StandardCellRules
// (external repos have no kernel/cell package to scan). New contributors: this
// is not a missed migration — it is a deliberate scope boundary.

import (
	"go/types"
	"strings"
	"testing"
)

// cellInitKernelCellPkgPath is the canonical import path of the kernel/cell package.
// Anchored to PlatformModulePath — not a bare string literal.
const cellInitKernelCellPkgPath = PlatformFrameworkModulePath + "/kernel/cell"

// CheckKernelCellDoesNotImportRuntime scans the kernel/cell package to confirm
// it imports neither runtime/* nor adapters/*. Returns diagnostics for each
// forbidden import found. Not registered in StandardCellRules (gocell-internal
// layout check, vacuous for external repos).
func CheckKernelCellDoesNotImportRuntime(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./framework/kernel/cell"}), func(p *Pass) []Diagnostic {
		diags = append(diags, scanPassForForbiddenImports(p, "kernel/cell")...)
		return nil
	})
	return diags
}

// scanPassForForbiddenImports returns a Diagnostic for each import in p that
// contains "runtime/" or "adapters/". The rel parameter is used as the Rel
// field of each diagnostic. Extracted so the detector logic can be exercised
// directly by RED fixture tests without wiring through the full
// Run(t, Typed(..., []string{"./framework/kernel/cell"})) scope.
func scanPassForForbiddenImports(p *Pass, rel string) []Diagnostic {
	if p == nil || p.Pkg == nil {
		return nil
	}
	var diags []Diagnostic
	for _, imp := range p.Pkg.Imports() {
		path := imp.Path()
		if strings.Contains(path, "runtime/") || strings.Contains(path, "adapters/") {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    0,
				Message: "kernel/cell must not import runtime/* or adapters/*; found: " + path,
			})
		}
	}
	return diags
}

// CheckKernelCellRegistrarDefinedHere confirms that the Registrar interface
// type is declared in kernel/cell (not aliased from another package). Not
// registered in StandardCellRules (gocell-internal layout check).
func CheckKernelCellRegistrarDefinedHere(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./framework/kernel/cell"}), func(p *Pass) []Diagnostic {
		diags = append(diags, scanPassForRegistrarLocality(p, "kernel/cell", cellInitKernelCellPkgPath)...)
		return nil
	})
	return diags
}

// scanPassForRegistrarLocality checks that "Registrar" in the scanned package
// is a locally-declared interface type (not an alias or a type from another
// package). rel is used as the Rel field; wantPkgPath is the expected package
// import path of the Registrar declaration. Extracted so RED fixture tests can
// call the detector directly without going through the "./framework/kernel/cell" scope.
func scanPassForRegistrarLocality(p *Pass, rel, wantPkgPath string) []Diagnostic {
	if p == nil || p.Pkg == nil {
		return nil
	}
	scope := p.Pkg.Scope()
	obj := scope.Lookup("Registrar")
	if obj == nil {
		return []Diagnostic{{Rel: rel, Line: 0, Message: "Registrar must be defined in kernel/cell"}}
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return []Diagnostic{{Rel: rel, Line: 0, Message: "Registrar must be a type name"}}
	}
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return []Diagnostic{{Rel: rel, Line: 0, Message: "Registrar must be a named type"}}
	}
	var diags []Diagnostic
	if _, ok = named.Underlying().(*types.Interface); !ok {
		diags = append(diags, Diagnostic{Rel: rel, Line: 0, Message: "Registrar must be an interface type"})
	}
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	if pkgPath != wantPkgPath {
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    0,
			Message: "Registrar must be defined in " + wantPkgPath + ", got " + pkgPath,
		})
	}
	return diags
}
