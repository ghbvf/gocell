// INVARIANT: PGQUERY-01

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const pgQueryBoundaryRule = "PGQUERY-01"

var legacyQueryFiles = []string{
	"pkg/query/builder.go",
	"pkg/query/builder_test.go",
	"pkg/query/keyset.go",
	"pkg/query/keyset_test.go",
}

var legacyQuerySymbols = []string{"Builder", "NewBuilder", "AppendKeyset"}

type pgQueryBoundaryViolation struct {
	File    string
	Line    int
	Message string
}

func (v pgQueryBoundaryViolation) String() string {
	if v.Line == 0 {
		return fmt.Sprintf("%s: %s: %s", pgQueryBoundaryRule, v.File, v.Message)
	}
	return fmt.Sprintf("%s: %s:%d: %s", pgQueryBoundaryRule, v.File, v.Line, v.Message)
}

func TestPGQueryBuilderSplitBoundary(t *testing.T) {
	root := findModuleRoot(t)
	module := readModulePath(t, root)

	violations, err := checkPGQueryBuilderSplit(root, module)
	require.NoError(t, err)

	for _, v := range violations {
		t.Log(v.String())
	}
	assert.Empty(t, violations,
		"PGQUERY-01: PostgreSQL SQL builder/keyset helpers belong in pkg/pgquery. "+
			"pkg/query may keep only generic pagination, cursor, runmode, and mempage helpers.")
}

func checkPGQueryBuilderSplit(root, module string) ([]pgQueryBoundaryViolation, error) {
	violations := append([]pgQueryBoundaryViolation{}, findLegacyQueryFiles(root)...)

	declViolations, err := findLegacyQuerySymbols(root)
	if err != nil {
		return nil, err
	}
	violations = append(violations, declViolations...)

	useViolations, err := findLegacyQueryBuilderUses(root, module)
	if err != nil {
		return nil, err
	}
	violations = append(violations, useViolations...)

	return violations, nil
}

func findLegacyQueryFiles(root string) []pgQueryBoundaryViolation {
	var violations []pgQueryBoundaryViolation
	for _, rel := range legacyQueryFiles {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			violations = append(violations, pgQueryBoundaryViolation{
				File:    rel,
				Message: "legacy pkg/query SQL builder/keyset file must move to pkg/pgquery",
			})
		}
	}
	return violations
}

func findLegacyQuerySymbols(root string) ([]pgQueryBoundaryViolation, error) {
	files, err := scanner.DirsScope(root, []string{"pkg/query"}, scanner.IncludeTests()).Files()
	if err != nil {
		return nil, err
	}

	var violations []pgQueryBoundaryViolation
	for _, path := range files {
		fileViolations, err := scanLegacyQuerySymbolDecls(root, path)
		if err != nil {
			return nil, err
		}
		violations = append(violations, fileViolations...)
	}
	return violations, nil
}

func scanLegacyQuerySymbolDecls(root, path string) ([]pgQueryBoundaryViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}

	var violations []pgQueryBoundaryViolation
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if slices.Contains(legacyQuerySymbols, d.Name.Name) {
				violations = append(violations, pgQueryBoundaryViolation{
					File:    relSlash(root, path),
					Line:    fset.Position(d.Pos()).Line,
					Message: fmt.Sprintf("legacy pkg/query symbol %s must live in pkg/pgquery", d.Name.Name),
				})
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !slices.Contains(legacyQuerySymbols, typeSpec.Name.Name) {
					continue
				}
				violations = append(violations, pgQueryBoundaryViolation{
					File:    relSlash(root, path),
					Line:    fset.Position(typeSpec.Pos()).Line,
					Message: fmt.Sprintf("legacy pkg/query symbol %s must live in pkg/pgquery", typeSpec.Name.Name),
				})
			}
		}
	}
	return violations, nil
}

func findLegacyQueryBuilderUses(root, module string) ([]pgQueryBoundaryViolation, error) {
	// IncludeGenerated: the legacy pkg/query symbol ban is module-wide, so
	// codegen output under generated/ must also be subject to the rule —
	// otherwise codegen could silently reintroduce a forbidden import.
	files, err := scanner.ModuleScope(root, scanner.IncludeTests(), scanner.IncludeGenerated()).Files()
	if err != nil {
		return nil, err
	}

	var violations []pgQueryBoundaryViolation
	for _, path := range files {
		rel := relSlash(root, path)
		if strings.HasPrefix(rel, "pkg/pgquery/") {
			continue
		}
		fileViolations, err := scanLegacyQueryBuilderUses(root, module, path)
		if err != nil {
			return nil, err
		}
		violations = append(violations, fileViolations...)
	}
	return violations, nil
}

func scanLegacyQueryBuilderUses(root, module, path string) ([]pgQueryBoundaryViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}

	aliases, dotImportLine := pkgQueryImportAliases(fset, file, module)
	if len(aliases) == 0 && dotImportLine == 0 {
		return nil, nil
	}

	rel := relSlash(root, path)
	var violations []pgQueryBoundaryViolation
	if dotImportLine != 0 {
		violations = append(violations, pgQueryBoundaryViolation{
			File:    rel,
			Line:    dotImportLine,
			Message: "dot-imports pkg/query; use pkg/pgquery for SQL builder/keyset helpers",
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !isLegacyQueryBuilderSymbol(sel.Sel.Name) {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := aliases[x.Name]; !ok {
			return true
		}
		violations = append(violations, pgQueryBoundaryViolation{
			File:    rel,
			Line:    fset.Position(sel.Pos()).Line,
			Message: fmt.Sprintf("uses pkg/query.%s; use pkg/pgquery.%s", sel.Sel.Name, sel.Sel.Name),
		})
		return true
	})

	return violations, nil
}

func pkgQueryImportAliases(fset *token.FileSet, file *ast.File, module string) (map[string]struct{}, int) {
	aliases := map[string]struct{}{}
	queryImport := module + "/pkg/query"
	var dotImportLine int

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != queryImport {
			continue
		}
		if imp.Name == nil {
			aliases["query"] = struct{}{}
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImportLine = fset.Position(imp.Pos()).Line
		case "_":
			continue
		default:
			aliases[imp.Name.Name] = struct{}{}
		}
	}
	return aliases, dotImportLine
}

func isLegacyQueryBuilderSymbol(name string) bool {
	return name == "NewBuilder" || name == "AppendKeyset"
}

func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
