package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const errcodeImportPath = "github.com/ghbvf/gocell/pkg/errcode"

// TestErrcodeLiteralConstructionBanned seals the Kind-based error model:
// callers outside pkg/errcode must use errcode.New/Wrap so every error chooses
// a transport Kind explicitly.
func TestErrcodeLiteralConstructionBanned(t *testing.T) {
	root := findModuleRoot(t)
	files, err := collectGoFiles(root)
	require.NoError(t, err)

	var violations []string
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "pkg/errcode/") {
			continue
		}
		hits, err := findErrcodeErrorLiterals(file)
		require.NoError(t, err, "scan %s", rel)
		for _, line := range hits {
			violations = append(violations,
				fmt.Sprintf("ERRCODE-KIND-LITERAL-01: %s:%d constructs errcode.Error directly; use errcode.New/Wrap", rel, line))
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations, "errcode.Error literal construction outside pkg/errcode is forbidden")
}

func findErrcodeErrorLiterals(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	errcodeNames := errcodeImportNames(f)
	if len(errcodeNames) == 0 {
		return nil, nil
	}

	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isErrcodeErrorType(lit.Type, errcodeNames) {
			return true
		}
		lines = append(lines, fset.Position(lit.Pos()).Line)
		return true
	})
	return lines, nil
}

func errcodeImportNames(f *ast.File) map[string]struct{} {
	names := map[string]struct{}{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcodeImportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				names[imp.Name.Name] = struct{}{}
			}
			continue
		}
		names["errcode"] = struct{}{}
	}
	return names
}

func isErrcodeErrorType(expr ast.Expr, errcodeNames map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Error" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = errcodeNames[pkg.Name]
	return ok
}
