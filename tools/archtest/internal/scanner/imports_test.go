package scanner_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func parseFileImports(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return f
}

func TestPackageAliases_DefaultName(t *testing.T) {
	f := parseFileImports(t, `package x
import "github.com/ghbvf/gocell/runtime/auth"
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if _, ok := got["auth"]; !ok || len(got) != 1 {
		t.Errorf("default name: got %v, want {auth}", got)
	}
}

func TestPackageAliases_Renamed(t *testing.T) {
	f := parseFileImports(t, `package x
import authpkg "github.com/ghbvf/gocell/runtime/auth"
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if _, ok := got["authpkg"]; !ok || len(got) != 1 {
		t.Errorf("renamed: got %v, want {authpkg}", got)
	}
}

func TestPackageAliases_BlankExcluded(t *testing.T) {
	f := parseFileImports(t, `package x
import _ "github.com/ghbvf/gocell/runtime/auth"
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if len(got) != 0 {
		t.Errorf("blank import: got %v, want empty", got)
	}
}

func TestPackageAliases_DotExcluded(t *testing.T) {
	f := parseFileImports(t, `package x
import . "github.com/ghbvf/gocell/runtime/auth"
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if len(got) != 0 {
		t.Errorf("dot import: got %v, want empty", got)
	}
}

func TestPackageAliases_MultipleSamePackage(t *testing.T) {
	f := parseFileImports(t, `package x
import (
	"github.com/ghbvf/gocell/runtime/auth"
	authpkg "github.com/ghbvf/gocell/runtime/auth"
)
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if _, ok := got["auth"]; !ok {
		t.Errorf("multi import: missing auth in %v", got)
	}
	if _, ok := got["authpkg"]; !ok {
		t.Errorf("multi import: missing authpkg in %v", got)
	}
	if len(got) != 2 {
		t.Errorf("multi import: got %d aliases, want 2", len(got))
	}
}

func TestPackageAliases_NoImports(t *testing.T) {
	f := parseFileImports(t, `package x
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if got == nil {
		t.Errorf("no imports: got nil, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("no imports: got %v, want empty", got)
	}
}

func TestPackageAliases_TargetMiss(t *testing.T) {
	f := parseFileImports(t, `package x
import "github.com/ghbvf/gocell/runtime/http"
`)
	got := scanner.PackageAliases(f, "github.com/ghbvf/gocell/runtime/auth")
	if len(got) != 0 {
		t.Errorf("target miss: got %v, want empty", got)
	}
}

func TestPackageAliases_NilFileEmpty(t *testing.T) {
	got := scanner.PackageAliases(nil, "github.com/ghbvf/gocell/runtime/auth")
	if got == nil {
		t.Errorf("nil file: got nil, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("nil file: got %v, want empty", got)
	}
}

func TestPackageAliases_EmptyImportPath(t *testing.T) {
	f := parseFileImports(t, `package x
import "github.com/ghbvf/gocell/runtime/auth"
`)
	got := scanner.PackageAliases(f, "")
	if len(got) != 0 {
		t.Errorf("empty importPath: got %v, want empty (no match)", got)
	}
}
