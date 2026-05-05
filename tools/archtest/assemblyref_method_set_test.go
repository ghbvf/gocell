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
// Each entry is "<name>(<params>) <results>" with go/printer-equivalent
// formatting (single space between groups, no trailing space).
var expectedAssemblyRefMethods = map[string]string{
	"ID":      "() string",
	"CellIDs": "() []string",
	"Cell":    "(id string) Cell",
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

// formatFuncSignature renders an *ast.FuncType as "(<params>) <results>"
// matching the canonical Go fmt-equivalent form used in
// expectedAssemblyRefMethods. A single anonymous result is rendered without
// parentheses; multiple results, or a single named result (e.g.
// "(c Cell)"), are parenthesized — matching `gofmt` output. When extending
// AssemblyRef, write the expected entry in the same form.
func formatFuncSignature(ft *ast.FuncType) string {
	out := "(" + formatFieldList(ft.Params) + ")"
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return out
	}
	results := formatFieldList(ft.Results)
	if len(ft.Results.List) > 1 || len(ft.Results.List[0].Names) > 0 {
		results = "(" + results + ")"
	}
	return out + " " + results
}

// formatFieldList renders an *ast.FieldList as a comma-joined string of
// "<names> <type>" segments (or just "<type>" when the field is anonymous).
// Embedded names within a single field share one type expression.
func formatFieldList(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typeStr := formatExpr(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typeStr)
			continue
		}
		names := ""
		for i, n := range f.Names {
			if i > 0 {
				names += ", "
			}
			names += n.Name
		}
		parts = append(parts, names+" "+typeStr)
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
