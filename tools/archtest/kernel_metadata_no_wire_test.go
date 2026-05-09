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
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
//
// SCANNER-ESCAPE-HATCH: deferred-scanner-migration
// Scans kernel/metadata/*.go via os.ReadDir; predates scanner framework,
// candidate for scanner.DirsScope migration.
func TestKernelMetadataDoesNotContainWireSymbols(t *testing.T) {
	root := findModuleRoot(t)
	metadataDir := filepath.Join(root, "kernel", "metadata")

	entries, err := os.ReadDir(metadataDir)
	require.NoError(t, err, "cannot read kernel/metadata/")

	var violations []wireViolation
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		absPath := filepath.Join(metadataDir, name)
		f, err := parser.ParseFile(fset, absPath, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "cannot parse %s", absPath)

		rel, _ := filepath.Rel(root, absPath)

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if kernelMetadataWireSymbols[d.Name.Name] {
					violations = append(violations, wireViolation{
						File:   rel,
						Line:   fset.Position(d.Pos()).Line,
						Symbol: d.Name.Name,
					})
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if kernelMetadataWireSymbols[s.Name.Name] {
							violations = append(violations, wireViolation{
								File:   rel,
								Line:   fset.Position(s.Pos()).Line,
								Symbol: s.Name.Name,
							})
						}
					case *ast.ValueSpec:
						for _, ident := range s.Names {
							if kernelMetadataWireSymbols[ident.Name] {
								violations = append(violations, wireViolation{
									File:   rel,
									Line:   fset.Position(ident.Pos()).Line,
									Symbol: ident.Name,
								})
							}
						}
					}
				}
			}
		}
	}

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
