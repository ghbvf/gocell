package archtest

// assemblyref_method_set_test.go — AST guard for cell.AssemblyRef method set.
//
// Rule:
//
//   ASSEMBLYREF-METHOD-SET-01  cell.AssemblyRef interface (kernel/cell/auth_plan.go)
//                              must declare exactly three methods:
//                                ID() string
//                                CellIDs() []string
//                                Cell(id string) Cell
//                              No additions, no removals. Reverse narrowing
//                              (e.g. dropping Cell(id) and re-introducing the
//                              implicit type-assertion bridge in
//                              runtime/bootstrap/auth_plan_apply.go) is the
//                              regression we are guarding against.
//
// Background: prior to refactor/529 the bootstrap layer carried a private
// assemblyWithCell sub-interface and an asmCellLookup helper that did
// asm.(assemblyWithCell) — a silent-skip type assertion when the assembly
// implementation lacked Cell(id). Promoting Cell(id) onto AssemblyRef makes
// the contract complete and lets the compiler enforce it; this test prevents
// a future refactor from re-narrowing the interface back to the two-method
// form.
//
// ref: kubernetes-sigs/controller-runtime pkg/cluster/cluster.go@main
//      — explicit interface seam, no implicit type narrowing in callers.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const assemblyRefRule01 = "ASSEMBLYREF-METHOD-SET-01"

// expectedAssemblyRefMethods is the canonical method set for cell.AssemblyRef.
// Each entry is "(<param-types>) <result-types>" — parameter and result
// names are intentionally elided so that this guard locks the capability
// (which methods exist with which types) rather than the source-level
// spelling (which is documentation). A pure parameter rename like
// `Cell(id string) Cell` → `Cell(cellID string) Cell` is intentionally
// allowed; adding/removing/retyping a method must update this map.
var expectedAssemblyRefMethods = map[string]string{
	"ID":      "() string",
	"CellIDs": "() []string",
	"Cell":    "(string) Cell",
}

// TestAssemblyRefMethodSet enforces ASSEMBLYREF-METHOD-SET-01: the
// cell.AssemblyRef interface must carry exactly the three methods listed in
// expectedAssemblyRefMethods. Adding, removing, or renaming a method without
// updating this test indicates either a deliberate contract change (update
// the test in the same PR) or an accidental narrowing (revert the change).
func TestAssemblyRefMethodSet(t *testing.T) {
	root := findModuleRoot(t)
	srcPath := filepath.Join(root, "kernel", "cell", "auth_plan.go")

	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, srcPath, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", srcPath)

	iface := findInterfaceDecl(af, "AssemblyRef")
	require.NotNil(t, iface, "%s: type AssemblyRef interface not found in %s", assemblyRefRule01, srcPath)

	got := make(map[string]string, len(iface.Methods.List))
	for _, m := range iface.Methods.List {
		require.Len(t, m.Names, 1,
			"%s: AssemblyRef methods must be named (no embedded interfaces)", assemblyRefRule01)
		name := m.Names[0].Name
		ft, ok := m.Type.(*ast.FuncType)
		require.True(t, ok, "%s: method %s must be a FuncType", assemblyRefRule01, name)
		got[name] = formatFuncSignature(ft)
	}

	assert.Equal(t, expectedAssemblyRefMethods, got,
		"%s: cell.AssemblyRef method set drift detected. "+
			"If this is a deliberate contract change, update expectedAssemblyRefMethods "+
			"in the same PR; otherwise revert the interface narrowing/widening.",
		assemblyRefRule01)
}

// findInterfaceDecl returns the *ast.InterfaceType declared as `type <name>
// interface { … }` in af, or nil if not found.
func findInterfaceDecl(af *ast.File, name string) *ast.InterfaceType {
	for _, decl := range af.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			if iface, ok := ts.Type.(*ast.InterfaceType); ok {
				return iface
			}
		}
	}
	return nil
}

// formatFuncSignature renders an *ast.FuncType as "(<param-types>) <results>"
// matching the canonical types-only form used in expectedAssemblyRefMethods.
// A single result is rendered without parentheses regardless of whether the
// AST carries a name; two or more results are parenthesized. Names of both
// parameters and results are deliberately discarded — see
// expectedAssemblyRefMethods doc.
func formatFuncSignature(ft *ast.FuncType) string {
	out := "(" + formatFieldTypes(ft.Params) + ")"
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return out
	}
	results := formatFieldTypes(ft.Results)
	if len(ft.Results.List) > 1 {
		results = "(" + results + ")"
	}
	return out + " " + results
}

// formatFieldTypes renders an *ast.FieldList as a comma-joined sequence of
// type expressions, ignoring all parameter / result names. A field that
// declares N names with a single type expression contributes N type
// segments (so `func(a, b string)` and `func(string, string)` both render
// as "string, string").
func formatFieldTypes(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typeStr := formatExpr(f.Type)
		// One field may declare multiple names sharing the same type; one
		// type segment per name keeps the rendering equivalent to the
		// fully-spelled form `func(a string, b string)`.
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			parts = append(parts, typeStr)
		}
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// formatExpr renders the small subset of ast.Expr shapes that appear in the
// AssemblyRef method set: identifiers, qualified identifiers, slice and
// fixed-size array types, and pointer types. Anything outside this set
// trips a panic — extending the interface should prompt extending this
// helper in the same change so that the archtest stays expressive instead
// of silently accepting an unfamiliar shape.
func formatExpr(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return formatExpr(v.X) + "." + v.Sel.Name
	case *ast.ArrayType:
		if v.Len == nil {
			return "[]" + formatExpr(v.Elt)
		}
		return "[" + formatExpr(v.Len) + "]" + formatExpr(v.Elt)
	case *ast.StarExpr:
		return "*" + formatExpr(v.X)
	case *ast.BasicLit:
		return v.Value
	}
	// Defensive: ASSEMBLYREF-METHOD-SET-01 should fail loudly on a method
	// signature that uses an expression shape this helper does not yet
	// handle, prompting a same-PR extension rather than silent acceptance.
	panic("assemblyref_method_set_test: unsupported ast.Expr shape; extend formatExpr when AssemblyRef gains a new signature kind")
}
