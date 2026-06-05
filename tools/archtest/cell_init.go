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
//
// Style note — why the detector helpers stay in _test.go:
//
// The two Check* wrappers here share their package with cell_init_test.go
// because their underlying scan helpers (the Go type-system queries) do not
// need to be compiled into the importable surface for external repos. Unlike
// cell_init_checknotnoop.go — which fully migrates its helpers out of the
// _test.go so that external consumers can call CheckCellL2InitCheckNotNoop via
// StandardCellRules — these two checks target GoCell's own kernel/cell layout
// and are intentionally NOT in StandardCellRules (external repos have no
// kernel/cell package to scan). Keeping the heavier type-query helpers in
// _test.go avoids polluting the importable archtest surface with gocell-
// internal symbols. New contributors: this is not a missed migration — it is
// a deliberate scope boundary.

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
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/cell"}), func(p *Pass) []Diagnostic {
		diags = append(diags, scanPassForForbiddenImports(p, "kernel/cell")...)
		return nil
	})
	return diags
}

// scanPassForForbiddenImports returns a Diagnostic for each import in p that
// contains "runtime/" or "adapters/". The rel parameter is used as the Rel
// field of each diagnostic. Extracted so the detector logic can be exercised
// directly by RED fixture tests without wiring through the full
// Run(t, Typed(..., []string{"./kernel/cell"})) scope.
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/cell"}), func(p *Pass) []Diagnostic {
		diags = append(diags, scanPassForRegistrarLocality(p, "kernel/cell", cellInitKernelCellPkgPath)...)
		return nil
	})
	return diags
}

// scanPassForRegistrarLocality checks that "Registrar" in the scanned package
// is a locally-declared interface type (not an alias or a type from another
// package). rel is used as the Rel field; wantPkgPath is the expected package
// import path of the Registrar declaration. Extracted so RED fixture tests can
// call the detector directly without going through the "./kernel/cell" scope.
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
