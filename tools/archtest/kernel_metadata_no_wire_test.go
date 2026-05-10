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
	"go/parser"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
	// defines wrapper.ContractSpec (a different type entirely).
}

type wireViolation struct {
	File   string
	Line   int
	Symbol string
}

// TestKernelMetadataDoesNotContainWireSymbols enforces KERNEL-METADATA-NO-WIRE-01.
// It parses all non-test .go files in kernel/metadata/ and fails if any of the
// forbidden wire-format top-level declaration names are found.
func TestKernelMetadataDoesNotContainWireSymbols(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"kernel/metadata"},
		scanner.MatchRels(func(rel string) bool {
			// Single-dir semantics: only files directly under kernel/metadata,
			// no sub-packages.
			return filepath.ToSlash(filepath.Dir(rel)) == "kernel/metadata"
		}),
	)

	var violations []wireViolation

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		f := fc.File
		rel := fc.Rel

		// Paired-index over f.Decls: top-level decls only; avoids path B's
		// `for _, X :=` + type-dispatch pattern.
		for i := range f.Decls {
			decl := f.Decls[i]
			if d, ok := decl.(*ast.FuncDecl); ok {
				if kernelMetadataWireSymbols[d.Name.Name] {
					violations = append(violations, wireViolation{
						File:   rel,
						Line:   fc.Fset.Position(d.Pos()).Line,
						Symbol: d.Name.Name,
					})
				}
				continue
			}
			d, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for j := range d.Specs {
				spec := d.Specs[j]
				if s, ok := spec.(*ast.TypeSpec); ok {
					if kernelMetadataWireSymbols[s.Name.Name] {
						violations = append(violations, wireViolation{
							File:   rel,
							Line:   fc.Fset.Position(s.Pos()).Line,
							Symbol: s.Name.Name,
						})
					}
					continue
				}
				if s, ok := spec.(*ast.ValueSpec); ok {
					for _, ident := range s.Names {
						if kernelMetadataWireSymbols[ident.Name] {
							violations = append(violations, wireViolation{
								File:   rel,
								Line:   fc.Fset.Position(ident.Pos()).Line,
								Symbol: ident.Name,
							})
						}
					}
				}
			}
		}
	})

	if len(violations) > 0 {
		t.Logf("%s: found %d wire-symbol declaration(s) in kernel/metadata/:",
			ruleKernelMetadataNoWire, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  symbol: %s", v.File, v.Line, v.Symbol)
		}
	}

	assert.Empty(t, violations,
		"%s: kernel/metadata must not declare wire-format symbols; "+
			"move them to runtime/devtools/catalog/", ruleKernelMetadataNoWire)
}
