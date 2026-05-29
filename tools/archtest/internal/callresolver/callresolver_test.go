package callresolver

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// typeCheck parses + type-checks src against the real stdlib (importer.Default)
// and returns the file, its *types.Info, and the FileSet. Mirrors
// scanSyntheticSource in audit_hash_input_frozen_test.go so IsCallToPkgFunc is
// exercised against genuine go/types resolution (alias / dot-import proof).
func typeCheck(t *testing.T, src string) (*ast.File, *types.Info, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("synthetic", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return file, info, fset
}

// parseOnly parses src without type-checking (for AST-only helpers).
func parseOnly(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return file, fset
}

// firstCallNamed returns the first CallExpr in file whose callee's trailing
// identifier (SelectorExpr.Sel or bare/indexed Ident) equals name.
func firstCallNamed(t *testing.T, file *ast.File, name string) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if calleeTrailingName(call.Fun) == name {
			found = call
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no call to %q found", name)
	}
	return found
}

func calleeTrailingName(fun ast.Expr) string {
	switch v := fun.(type) {
	case *ast.SelectorExpr:
		if v.Sel != nil {
			return v.Sel.Name
		}
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr:
		return calleeTrailingName(v.X)
	case *ast.IndexListExpr:
		return calleeTrailingName(v.X)
	}
	return ""
}

// firstFuncDecl returns the first top-level FuncDecl named name.
func firstFuncDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name != nil && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("no FuncDecl %q", name)
	return nil
}

// --- IsCallToPkgFunc -------------------------------------------------------

func TestIsCallToPkgFunc(t *testing.T) {
	t.Parallel()

	const qualified = `package p
import (
	"crypto/hmac"
	"crypto/sha256"
)
func use(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	return m.Sum(nil)
}`
	const aliased = `package p
import (
	h "crypto/hmac"
	"crypto/sha256"
)
func use(key []byte) []byte {
	m := h.New(sha256.New, key)
	return m.Sum(nil)
}`
	const dotImport = `package p
import (
	. "crypto/hmac"
	"crypto/sha256"
)
func use(key []byte) []byte {
	m := New(sha256.New, key)
	return m.Sum(nil)
}`
	const generic = `package p
func Foo[T any]() int { return 0 }
func Pair[K any, V any]() int { return 0 }
func use() {
	_ = Foo[int]()
	_ = Pair[int, string]()
}`
	const method = `package p
type T struct{}
func (T) New() int { return 0 }
func use(v T) { _ = v.New() }`
	// NOTE: Go forbids generic methods ("method must have no type parameters"),
	// so an IndexExpr over a *method* selector (val.Method[T]()) cannot occur in
	// real source — the method→false path is covered by the non-generic
	// "method-call-not-pkg-func" case below; unwrapCallee's IndexExpr branch is
	// exercised by the package-level generic funcs (Foo[int] / Pair[int,string]).

	cases := []struct {
		name    string
		src     string
		callee  string
		pkgPath string
		fnName  string
		want    bool
	}{
		{"qualified-hmac-new", qualified, "New", "crypto/hmac", "New", true},
		{"alias-hmac-new", aliased, "New", "crypto/hmac", "New", true},
		{"dot-import-hmac-new", dotImport, "New", "crypto/hmac", "New", true},
		{"generic-indexexpr", generic, "Foo", "synthetic", "Foo", true},
		{"generic-indexlistexpr", generic, "Pair", "synthetic", "Pair", true},
		{"wrong-name", qualified, "New", "crypto/hmac", "Equal", false},
		{"wrong-pkg", qualified, "New", "crypto/subtle", "New", false},
		{"method-call-not-pkg-func", method, "New", "synthetic", "New", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, info, _ := typeCheck(t, tc.src)
			call := firstCallNamed(t, file, tc.callee)
			if got := IsCallToPkgFunc(info, call, tc.pkgPath, tc.fnName); got != tc.want {
				t.Errorf("IsCallToPkgFunc(%s, %q, %q) = %v, want %v",
					tc.name, tc.pkgPath, tc.fnName, got, tc.want)
			}
		})
	}
}

func TestIsCallToPkgFunc_NilGuards(t *testing.T) {
	t.Parallel()
	file, info, _ := typeCheck(t, `package p
import "crypto/hmac"
import "crypto/sha256"
func use(k []byte) { _ = hmac.New(sha256.New, k) }`)
	call := firstCallNamed(t, file, "New")
	if IsCallToPkgFunc(nil, call, "crypto/hmac", "New") {
		t.Error("nil info must yield false")
	}
	if IsCallToPkgFunc(info, nil, "crypto/hmac", "New") {
		t.Error("nil call must yield false")
	}
}

// --- HasReceiver -----------------------------------------------------------

func TestHasReceiver(t *testing.T) {
	t.Parallel()
	const src = `package p
type Protocol struct{}
type Store[T any] struct{}
type Pair[K any, V any] struct{}
func (p *Protocol) PtrM() {}
func (p Protocol) ValM() {}
func (s *Store[T]) StoreM() {}
func (s Pair[K, V]) PairM() {}
func Free() {}
`
	file, _ := parseOnly(t, src)

	cases := []struct {
		fn       string
		typeName string
		want     bool
	}{
		{"PtrM", "Protocol", true},
		{"ValM", "Protocol", true},
		{"StoreM", "Store", true},
		{"PairM", "Pair", true},
		{"PtrM", "Wrong", false},
		{"Free", "Protocol", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.fn+"/"+tc.typeName, func(t *testing.T) {
			t.Parallel()
			fd := firstFuncDecl(t, file, tc.fn)
			if got := HasReceiver(fd, tc.typeName); got != tc.want {
				t.Errorf("HasReceiver(%s, %q) = %v, want %v", tc.fn, tc.typeName, got, tc.want)
			}
		})
	}
	if HasReceiver(nil, "Protocol") {
		t.Error("nil fn must yield false")
	}
}

// --- WalkFuncDecls ---------------------------------------------------------

func TestWalkFuncDecls_BodySkipAndCount(t *testing.T) {
	t.Parallel()
	// ext() is an external/bodiless decl (valid Go syntax); must be skipped.
	const src = `package p
func a() {}
func b() { _ = func() {} }
func ext()
`
	file, fset := parseOnly(t, src)
	var visited []string
	WalkFuncDecls([]*ast.File{file}, nil, fset, func(*ast.File) string { return "rel.go" },
		func(ctx FuncDeclContext) {
			visited = append(visited, ctx.Func.Name.Name)
			if ctx.Rel != "rel.go" {
				t.Errorf("ctx.Rel = %q, want rel.go", ctx.Rel)
			}
			if ctx.Info != nil {
				t.Error("AST-only walk must carry nil Info")
			}
			if ctx.File != file {
				t.Error("ctx.File mismatch")
			}
		})
	// a and b only (ext is bodiless; the nested FuncLit in b is not a FuncDecl).
	if len(visited) != 2 || visited[0] != "a" || visited[1] != "b" {
		t.Errorf("visited = %v, want [a b]", visited)
	}
}

func TestWalkFuncDecls_TypedInfoIsBound(t *testing.T) {
	t.Parallel()
	file, info, fset := typeCheck(t, `package p
func target() int { return 1 }`)
	var count int
	WalkFuncDecls([]*ast.File{file}, info, fset, nil, func(ctx FuncDeclContext) {
		count++
		if ctx.Rel != "" {
			t.Errorf("nil rel must yield empty Rel, got %q", ctx.Rel)
		}
		obj := ctx.Info.Defs[ctx.Func.Name]
		if _, ok := obj.(*types.Func); !ok {
			t.Errorf("ctx.Info.Defs[%s] = %T, want *types.Func", ctx.Func.Name.Name, obj)
		}
	})
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestWalkFuncDecls_MultiFile(t *testing.T) {
	t.Parallel()
	f1, fset := parseOnly(t, "package p\nfunc x() {}")
	f2, _ := parseOnly(t, "package p\nfunc y() {}")
	seen := map[string]bool{}
	WalkFuncDecls([]*ast.File{f1, f2}, nil, fset, nil, func(ctx FuncDeclContext) {
		seen[ctx.Func.Name.Name] = true
	})
	if !seen["x"] || !seen["y"] || len(seen) != 2 {
		t.Errorf("seen = %v, want {x,y}", seen)
	}
}

func TestWalkFuncDecls_NilSafe(t *testing.T) {
	t.Parallel()
	called := false
	WalkFuncDecls(nil, nil, nil, nil, func(FuncDeclContext) { called = true })
	WalkFuncDecls([]*ast.File{nil}, nil, nil, nil, func(FuncDeclContext) { called = true })
	if called {
		t.Error("nil/empty input must not invoke fn")
	}
	// nil fn with real FuncDecls must not panic (engine guards fn == nil).
	f, fset := parseOnly(t, "package p\nfunc x() {}")
	WalkFuncDecls([]*ast.File{f}, nil, fset, nil, nil)
}

// TestUnwrapCallee directly exercises the generic-unwrap helper, including the
// default→nil branch (a parenthesized callee, which IsCallToPkgFunc then treats
// as a non-match — mirroring the donor unwrapCalleeForResolve semantics).
func TestUnwrapCallee(t *testing.T) {
	t.Parallel()
	mustExpr := func(s string) ast.Expr {
		e, err := parser.ParseExpr(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return e
	}
	nonNil := []string{"pkg.Foo", "Foo", "pkg.Foo[int]", "pkg.Foo[int, string]"}
	for _, s := range nonNil {
		if unwrapCallee(mustExpr(s)) == nil {
			t.Errorf("unwrapCallee(%q) = nil, want non-nil", s)
		}
	}
	// default branch: ParenExpr (and any other shape) is not unwrapped → nil.
	if got := unwrapCallee(mustExpr("(pkg.Foo)")); got != nil {
		t.Errorf("unwrapCallee parenthesized callee = %T, want nil (default branch)", got)
	}
}
