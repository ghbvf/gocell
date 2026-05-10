package scanner_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// parseSrc is a test helper that parses Go source and fails fast on error.
// The token.FileSet is not returned because no test currently needs position
// information; if a future case wants line numbers, switch to returning
// (*token.FileSet, *ast.File) and update callers.
func parseSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func TestEachNode_PreorderSequence_File(t *testing.T) {
	t.Parallel()
	src := `package fake
func first() { _ = 1 }
func second() { _ = 2 }
`
	file := parseSrc(t, src)
	var names []string
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		names = append(names, fn.Name.Name)
	})
	want := []string{"first", "second"}
	if len(names) != len(want) {
		t.Fatalf("got %d FuncDecls, want %d: %v", len(names), len(want), names)
	}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("preorder[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestEachNode_NestedFuncLitVisited(t *testing.T) {
	t.Parallel()
	src := `package fake
func outer() {
	_ = func() { _ = func() { _ = 1 }() }
}
`
	file := parseSrc(t, src)
	var count int
	scanner.EachNode[ast.FuncLit](file, func(*ast.FuncLit) { count++ })
	if count != 2 {
		t.Errorf("expected 2 FuncLit visits (outer + nested), got %d", count)
	}
}

func TestEachNode_SubTreeScope(t *testing.T) {
	t.Parallel()
	src := `package fake
var globalIdent = 1
func outer() {
	localIdent := 2
	_ = localIdent
}
`
	file := parseSrc(t, src)

	// File-level: visits both globalIdent and localIdent (and others).
	var fileIdents []string
	scanner.EachNode[ast.Ident](file, func(id *ast.Ident) {
		fileIdents = append(fileIdents, id.Name)
	})
	hasGlobal := false
	hasLocal := false
	for _, n := range fileIdents {
		if n == "globalIdent" {
			hasGlobal = true
		}
		if n == "localIdent" {
			hasLocal = true
		}
	}
	if !hasGlobal || !hasLocal {
		t.Errorf("file-level should visit both global and local; got %v", fileIdents)
	}

	// Sub-tree: pick outer's body — should NOT include globalIdent.
	var bodyIdents []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "outer" {
			continue
		}
		scanner.EachNode[ast.Ident](fn.Body, func(id *ast.Ident) {
			bodyIdents = append(bodyIdents, id.Name)
		})
	}
	for _, n := range bodyIdents {
		if n == "globalIdent" {
			t.Errorf("sub-tree should not include globalIdent, got %v", bodyIdents)
		}
	}
	if len(bodyIdents) == 0 {
		t.Error("sub-tree should still visit local identifiers")
	}
}

func TestEachNode_NilRootIsNoOp(t *testing.T) {
	t.Parallel()
	called := false
	scanner.EachNode[ast.Ident](nil, func(*ast.Ident) { called = true })
	if called {
		t.Error("EachNode with nil root must be a no-op")
	}
}

func TestEachNode_GenericsReceiverFuncDecl(t *testing.T) {
	t.Parallel()
	src := `package fake
type Store[K any, V any] struct{}
func (s *Store[K, V]) Put(k K, v V) {}
func (s *Store[K, V]) Get(k K) (V, bool) { var z V; return z, false }
`
	file := parseSrc(t, src)
	var methods []string
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv == nil {
			return
		}
		methods = append(methods, fn.Name.Name)
	})
	want := []string{"Put", "Get"}
	if len(methods) != 2 {
		t.Fatalf("expected 2 methods on generic receiver, got %d: %v", len(methods), methods)
	}
	for i := range methods {
		if methods[i] != want[i] {
			t.Errorf("methods[%d] = %q, want %q", i, methods[i], want[i])
		}
	}
}

func TestEachNode_TypeAssertionInLoopVisited(t *testing.T) {
	t.Parallel()
	// Path B's target pattern: ensure EachNode reaches *ast.TypeAssertExpr nodes
	// nested inside RangeStmt bodies. Path B guard scans for these.
	src := `package fake
import "go/ast"
func _(file *ast.File) {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			_ = fn
		}
	}
}
`
	file := parseSrc(t, src)
	var taCount int
	scanner.EachNode[ast.TypeAssertExpr](file, func(*ast.TypeAssertExpr) {
		taCount++
	})
	if taCount != 1 {
		t.Errorf("expected 1 TypeAssertExpr inside RangeStmt body, got %d", taCount)
	}
}

// TestEachNode_TypeParamConstraint documents the compile-time guarantee that
// the type parameter constraint `interface { *S; ast.Node }` rejects:
//   - non-pointer types (e.g. ast.CallExpr without the *)
//   - non-ast types
//   - interface ast types like ast.Expr (the *S clause requires concrete pointer)
//
// We cannot exercise compile errors at runtime; the test merely lists the
// known-good usages that DO compile, locking the API shape.
func TestEachNode_TypeParamConstraint(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func _() {}
`)
	// All these compile-and-run: concrete *ast.<Node> pointer type families.
	scanner.EachNode[ast.FuncDecl](file, func(*ast.FuncDecl) {})
	scanner.EachNode[ast.CallExpr](file, func(*ast.CallExpr) {})
	scanner.EachNode[ast.Ident](file, func(*ast.Ident) {})
	scanner.EachNode[ast.SelectorExpr](file, func(*ast.SelectorExpr) {})
	scanner.EachNode[ast.RangeStmt](file, func(*ast.RangeStmt) {})
	scanner.EachNode[ast.TypeAssertExpr](file, func(*ast.TypeAssertExpr) {})
	// Non-compiling examples (kept as comments to document constraint failures):
	//   scanner.EachNode[ast.Expr](...)         // ast.Expr is interface; *S requires concrete struct
	//   scanner.EachNode[int](...)              // *int does not satisfy ast.Node
	//   scanner.EachNode[*ast.CallExpr](...)    // S is the value type, not the pointer; this would yield N=**ast.CallExpr
}
