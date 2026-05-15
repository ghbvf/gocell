// INVARIANT: KERNEL-METADATA-NO-WIRE-01
//
// KERNEL-METADATA-NO-WIRE-01 — invariant-driven gate.
//
// Invariant: kernel/metadata/*.go must not declare any wire-format symbols.
// All wire types (Document, Entity, BuildDocument, MarshalDocument,
// IncludeOptions, ExportOptions, PackageDepsView, CellDepGraph, CellEdge,
// CellSpec, SliceSpec, AssemblySpec, JourneySpec, Filter, StatusBoardEntry
// as wire type, etc.) must live exclusively in runtime/devtools/catalog/.
//
// This gate checks top-level declarations (type, func, var, const) in
// kernel/metadata/*.go (excluding *_test.go) against the forbidden symbol
// list. It does NOT scan function bodies — the concern is exported API surface.
//
// Rule: KERNEL-METADATA-NO-WIRE-01
package archtest

import (
	"go/ast"
	"path/filepath"
	"testing"
)

const ruleKernelMetadataNoWire = "KERNEL-METADATA-NO-WIRE-01"

// kernelMetadataWireSymbols is the canonical forbidden list. These are the
// type names, function names, and constants that must NOT appear as top-level
// declarations in kernel/metadata/*.go.
var kernelMetadataWireSymbols = map[string]bool{
	"Document":        true,
	"Entity":          true,
	"BuildDocument":   true,
	"MarshalDocument": true,
	"IncludeOptions":  true,
	"IncludeMask":     true,
	"ExportOptions":   true,
	"PackageDepsView": true,
	"CellDepGraph":    true,
	"CellEdge":        true,
	"CellSpec":        true,
	"SliceSpec":       true,
	"AssemblySpec":    true,
	"JourneySpec":     true,
	// Note: "Filter" is intentionally excluded here because many packages
	// may use "Filter" as a type name — we only forbid the wire-specific
	// names above. ContractSpec is also excluded because kernel/wrapper
	// defines contractspec.ContractSpec (a different type entirely).
}

// TestKernelMetadataDoesNotContainWireSymbols enforces KERNEL-METADATA-NO-WIRE-01.
// It parses all non-test .go files in kernel/metadata/ and fails if any of the
// forbidden wire-format top-level declaration names are found.
func TestKernelMetadataDoesNotContainWireSymbols(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"kernel/metadata"},
		MatchRels(func(rel string) bool {
			// Single-dir semantics: only files directly under kernel/metadata,
			// no sub-packages.
			return filepath.ToSlash(filepath.Dir(rel)) == "kernel/metadata"
		}),
	)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInChildren[ast.FuncDecl](file, func(d *ast.FuncDecl) {
				if kernelMetadataWireSymbols[d.Name.Name] {
					ds = append(ds, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(d.Pos()).Line,
						Message: "wire-format symbol " + d.Name.Name + " must not be declared in kernel/metadata; move to runtime/devtools/catalog/",
					})
				}
			})
			EachInChildren[ast.GenDecl](file, func(d *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](d, func(s *ast.TypeSpec) {
					if kernelMetadataWireSymbols[s.Name.Name] {
						ds = append(ds, Diagnostic{
							Rel:     rel,
							Line:    p.Fset.Position(s.Pos()).Line,
							Message: "wire-format symbol " + s.Name.Name + " must not be declared in kernel/metadata; move to runtime/devtools/catalog/",
						})
					}
				})
				EachInChildren[ast.ValueSpec](d, func(s *ast.ValueSpec) {
					for _, ident := range s.Names {
						if kernelMetadataWireSymbols[ident.Name] {
							ds = append(ds, Diagnostic{
								Rel:     rel,
								Line:    p.Fset.Position(ident.Pos()).Line,
								Message: "wire-format symbol " + ident.Name + " must not be declared in kernel/metadata; move to runtime/devtools/catalog/",
							})
						}
					}
				})
			})
		}
		return ds
	})
	Report(t, ruleKernelMetadataNoWire, diags)
}
