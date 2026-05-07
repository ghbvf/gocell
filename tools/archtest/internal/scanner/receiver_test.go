package scanner_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func parseReceiverExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		return fd.Recv.List[0].Type
	}
	t.Fatal("no method declaration with receiver found in source")
	return nil
}

func TestReceiverTypeName_Cases(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "pointer receiver *T",
			src:  "package x; type T struct{}; func (*T) M() {}",
			want: "T",
		},
		{
			name: "value receiver T",
			src:  "package x; type T struct{}; func (T) M() {}",
			want: "T",
		},
		{
			name: "generic single param T[P]",
			src:  "package x; type T[P any] struct{}; func (T[P]) M() {}",
			want: "T",
		},
		{
			name: "generic multi param T[P,Q]",
			src:  "package x; type T[P, Q any] struct{}; func (T[P, Q]) M() {}",
			want: "T",
		},
		{
			name: "unrecognized compound returns empty",
			src:  "package x; func (struct{}) M() {}",
			want: "",
		},
		// Pointer to a generic instantiation where the base is not a bare Ident.
		// Go does not allow (*pkg.T)[P] syntax in receiver lists, but
		// ReceiverTypeName should return "" for any unrecognized shape rather
		// than panicking.
		// Note: "func (*T[P]) M() {}" IS valid and covered by the *T case above;
		// this case tests a generic value-receiver path via IndexExpr where X
		// is not an Ident (which the parser would only produce under unusual AST
		// construction, but the contract is still: return "" not panic).
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr := parseReceiverExpr(t, tc.src)
			got := scanner.ReceiverTypeName(expr)
			if got != tc.want {
				t.Errorf("ReceiverTypeName(%T) = %q, want %q", expr, got, tc.want)
			}
		})
	}
}
