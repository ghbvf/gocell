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

// firstNodeOfKind is a test-only helper: returns the first node of the given
// pointer-to-struct type found by preorder traversal of root. Tests use this
// to dig into nested AST positions (e.g. the outer CompositeLit, the func
// body, the select stmt) without coupling to file.Decls index arithmetic.
//
// Built on EachInSubtree (with a sentinel early-return via a closure) rather
// than a paired ast.Preorder loop — this is the typed-walk dogfood that the
// rest of the test suite relies on.
func firstNodeOfKind[S any, N interface {
	*S
	ast.Node
}](root ast.Node) (N, bool) {
	var found N
	gotIt := false
	scanner.EachInSubtree[S, N](root, func(n N) {
		if gotIt {
			return
		}
		found = n
		gotIt = true
	})
	return found, gotIt
}

// -----------------------------------------------------------------------
// Subtree-vs-children dual-semantic matrix (T1..T9).
//
// T1+T2 share a nested CompositeLit fixture: subtree finds the inner KV,
// children does not. This is the anchor for "walk depth is a typed function
// choice" — the same fixture, different API name, different result.
//
// T3 nails down "root is never matched by EachInChildren even if its own
// type matches N". T4 nil-root no-op. T5 type-mismatch empty no-panic.
// T6/T7 confirm depth-1 boundaries for File and BlockStmt containers.
// T8/T9 confirm depth-1 over TypeSwitchStmt.Body / SelectStmt.Body — the
// exact container shapes that the PR's high-risk migration sites depend on.
// -----------------------------------------------------------------------

const nestedCompositeLitSrc = `package fake
func _() {
	_ = Outer{ A: Sub{ B: 1 } }
}
`

func TestEachInSubtree_NestedCompositeLit_KV_Finds2(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, nestedCompositeLitSrc)
	outer, ok := firstNodeOfKind[ast.CompositeLit](file)
	if !ok {
		t.Fatal("setup: outer CompositeLit not found in fixture")
	}
	var count int
	scanner.EachInSubtree[ast.KeyValueExpr](outer, func(*ast.KeyValueExpr) { count++ })
	if count != 2 {
		t.Errorf("EachInSubtree[KeyValueExpr] on nested CompositeLit: got %d, want 2 (outer A + inner B)", count)
	}
}

func TestEachInChildren_NestedCompositeLit_KV_OnlyTopLevel(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, nestedCompositeLitSrc)
	outer, ok := firstNodeOfKind[ast.CompositeLit](file)
	if !ok {
		t.Fatal("setup: outer CompositeLit not found in fixture")
	}
	var count int
	scanner.EachInChildren[ast.KeyValueExpr](outer, func(*ast.KeyValueExpr) { count++ })
	if count != 1 {
		t.Errorf("EachInChildren[KeyValueExpr] on nested CompositeLit: got %d, want 1 (only outer A; inner B is grandchild)", count)
	}
}

func TestEachInChildren_RootSelfNotMatched(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
var _ = SomeLit{}
`)
	cl, ok := firstNodeOfKind[ast.CompositeLit](file)
	if !ok {
		t.Fatal("setup: CompositeLit not found in fixture")
	}
	// Pass the CompositeLit as root and ask for CompositeLit children: root
	// itself must NOT count as a match (depth = 1 over children).
	var count int
	scanner.EachInChildren[ast.CompositeLit](cl, func(*ast.CompositeLit) { count++ })
	if count != 0 {
		t.Errorf("EachInChildren must not match root itself; got %d, want 0", count)
	}
}

func TestEachInChildren_NilRootNoOp(t *testing.T) {
	t.Parallel()
	called := false
	scanner.EachInChildren[ast.Ident](nil, func(*ast.Ident) { called = true })
	if called {
		t.Error("EachInChildren with nil root must be a no-op")
	}
}

func TestEachInChildren_TypeMismatchEmptyNoPanic(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func _() {}
`)
	fn, ok := firstNodeOfKind[ast.FuncDecl](file)
	if !ok {
		t.Fatal("setup: FuncDecl not found in fixture")
	}
	// fn.Body is *ast.BlockStmt; its direct children are top-level Stmts.
	// Asking for *ast.FuncDecl children must yield zero (no FuncDecl can be a
	// direct child of a BlockStmt — Decl is interface and the constraint
	// rejects ast.Decl entirely; FuncDecl is the closest concrete proxy) and
	// must not panic.
	var count int
	scanner.EachInChildren[ast.FuncDecl](fn.Body, func(*ast.FuncDecl) { count++ })
	if count != 0 {
		t.Errorf("BlockStmt has no FuncDecl children; got %d, want 0", count)
	}
}

func TestEachInChildren_FileTopLevelDecls(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
type T struct{}
func F() { type Inner struct{} }
`)
	var decls []string
	scanner.EachInChildren[ast.GenDecl](file, func(g *ast.GenDecl) {
		decls = append(decls, "GenDecl")
		_ = g
	})
	scanner.EachInChildren[ast.FuncDecl](file, func(f *ast.FuncDecl) {
		decls = append(decls, "FuncDecl:"+f.Name.Name)
	})
	// File's direct decls are top-level type T (GenDecl) + func F (FuncDecl).
	// The Inner struct declaration nested inside F's body must NOT appear.
	wantGen := 1
	wantFunc := 1
	gotGen, gotFunc := 0, 0
	for _, d := range decls {
		switch d {
		case "GenDecl":
			gotGen++
		case "FuncDecl:F":
			gotFunc++
		default:
			t.Errorf("unexpected decl entry %q (children-only should not leak into FuncDecl.Body)", d)
		}
	}
	if gotGen != wantGen || gotFunc != wantFunc {
		t.Errorf("file top-level decls: gen=%d (want %d), func=%d (want %d); full=%v",
			gotGen, wantGen, gotFunc, wantFunc, decls)
	}
}

func TestEachInChildren_BlockStmtTopLevelIfStmt(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func F() {
	if true { _ = 1 }
	for {}
	{
		if false { _ = 2 }
	}
}
`)
	fn, ok := firstNodeOfKind[ast.FuncDecl](file)
	if !ok {
		t.Fatal("setup: FuncDecl not found in fixture")
	}
	// fn.Body.List has 3 statements: IfStmt (if true), ForStmt, BlockStmt{...}.
	// Only the first IfStmt is a direct child; the IfStmt nested inside the
	// inner BlockStmt is a grandchild and must NOT appear.
	var count int
	scanner.EachInChildren[ast.IfStmt](fn.Body, func(*ast.IfStmt) { count++ })
	if count != 1 {
		t.Errorf("BlockStmt direct IfStmt children: got %d, want 1 (inner-block if must not leak)", count)
	}
}

func TestEachInChildren_TypeSwitchBody_CaseClause(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func F(x any) {
	switch x.(type) {
	case int:
	case string:
	}
}
`)
	ts, ok := firstNodeOfKind[ast.TypeSwitchStmt](file)
	if !ok {
		t.Fatal("setup: TypeSwitchStmt not found in fixture")
	}
	var count int
	scanner.EachInChildren[ast.CaseClause](ts.Body, func(*ast.CaseClause) { count++ })
	if count != 2 {
		t.Errorf("TypeSwitchStmt.Body direct CaseClause children: got %d, want 2", count)
	}
}

func TestEachInChildren_SelectBody_CommClause(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func F() {
	c1 := make(chan int)
	c2 := make(chan int)
	select {
	case <-c1:
	case <-c2:
	}
	_ = c1
	_ = c2
}
`)
	sel, ok := firstNodeOfKind[ast.SelectStmt](file)
	if !ok {
		t.Fatal("setup: SelectStmt not found in fixture")
	}
	var count int
	scanner.EachInChildren[ast.CommClause](sel.Body, func(*ast.CommClause) { count++ })
	if count != 2 {
		t.Errorf("SelectStmt.Body direct CommClause children: got %d, want 2", count)
	}
}

// -----------------------------------------------------------------------
// EachInSubtree behavioral tests carried forward from the old EachNode
// suite, renamed only. These lock the "preorder over the full sub-tree"
// contract that the new EachInSubtree inherits from the previous EachNode.
// -----------------------------------------------------------------------

func TestEachInSubtree_PreorderSequence_File(t *testing.T) {
	t.Parallel()
	src := `package fake
func first() { _ = 1 }
func second() { _ = 2 }
`
	file := parseSrc(t, src)
	var names []string
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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

func TestEachInSubtree_NestedFuncLitVisited(t *testing.T) {
	t.Parallel()
	src := `package fake
func outer() {
	_ = func() { _ = func() { _ = 1 }() }
}
`
	file := parseSrc(t, src)
	var count int
	scanner.EachInSubtree[ast.FuncLit](file, func(*ast.FuncLit) { count++ })
	if count != 2 {
		t.Errorf("expected 2 FuncLit visits (outer + nested), got %d", count)
	}
}

func TestEachInSubtree_SubTreeScope(t *testing.T) {
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
	scanner.EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
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
		scanner.EachInSubtree[ast.Ident](fn.Body, func(id *ast.Ident) {
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

func TestEachInSubtree_NilRootIsNoOp(t *testing.T) {
	t.Parallel()
	called := false
	scanner.EachInSubtree[ast.Ident](nil, func(*ast.Ident) { called = true })
	if called {
		t.Error("EachInSubtree with nil root must be a no-op")
	}
}

func TestEachInSubtree_GenericsReceiverFuncDecl(t *testing.T) {
	t.Parallel()
	src := `package fake
type Store[K any, V any] struct{}
func (s *Store[K, V]) Put(k K, v V) {}
func (s *Store[K, V]) Get(k K) (V, bool) { var z V; return z, false }
`
	file := parseSrc(t, src)
	var methods []string
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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

func TestEachInSubtree_TypeAssertionInLoopVisited(t *testing.T) {
	t.Parallel()
	// Path B's target pattern: ensure EachInSubtree reaches *ast.TypeAssertExpr
	// nodes nested inside RangeStmt bodies. Path B guard scans for these.
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
	scanner.EachInSubtree[ast.TypeAssertExpr](file, func(*ast.TypeAssertExpr) {
		taCount++
	})
	if taCount != 1 {
		t.Errorf("expected 1 TypeAssertExpr inside RangeStmt body, got %d", taCount)
	}
}

// TestEachInSubtree_TypeParamConstraint documents the compile-time guarantee
// that the type parameter constraint `interface { *S; ast.Node }` rejects:
//   - non-pointer types (e.g. ast.CallExpr without the *)
//   - non-ast types
//   - interface ast types like ast.Expr (the *S clause requires concrete pointer)
//
// We cannot exercise compile errors at runtime; the test merely lists the
// known-good usages that DO compile, locking the API shape. Same constraint
// applies to EachInChildren by construction.
func TestEachInSubtree_TypeParamConstraint(t *testing.T) {
	t.Parallel()
	file := parseSrc(t, `package fake
func _() {}
`)
	// All these compile-and-run: concrete *ast.<Node> pointer type families.
	scanner.EachInSubtree[ast.FuncDecl](file, func(*ast.FuncDecl) {})
	scanner.EachInSubtree[ast.CallExpr](file, func(*ast.CallExpr) {})
	scanner.EachInSubtree[ast.Ident](file, func(*ast.Ident) {})
	scanner.EachInSubtree[ast.SelectorExpr](file, func(*ast.SelectorExpr) {})
	scanner.EachInSubtree[ast.RangeStmt](file, func(*ast.RangeStmt) {})
	scanner.EachInSubtree[ast.TypeAssertExpr](file, func(*ast.TypeAssertExpr) {})
	scanner.EachInChildren[ast.FuncDecl](file, func(*ast.FuncDecl) {})
	// Non-compiling examples (kept as comments to document constraint failures):
	//   scanner.EachInSubtree[ast.Expr](...)         // ast.Expr is interface; *S requires concrete struct
	//   scanner.EachInChildren[int](...)             // *int does not satisfy ast.Node
	//   scanner.EachInSubtree[*ast.CallExpr](...)    // S is the value type, not the pointer; this would yield N=**ast.CallExpr
}
