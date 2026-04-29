package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var dotImportProductionDirs = []string{
	"adapters",
	"cells",
	"cmd",
	"examples",
	"kernel",
	"pkg",
	"runtime",
}

func TestProductionGoFilesDoNotUseDotImports(t *testing.T) {
	root := findModuleRoot(t)
	violations, err := dotImportViolations(root)
	require.NoError(t, err)
	assert.Empty(t, violations, "production Go files must not use dot imports")
}

func TestDotImportViolationsRejectsOnlyDotImports(t *testing.T) {
	root := t.TempDir()
	writeArchtestFile(t, root, "root_bad.go", `package rootpkg

import . "context"

func root() { _ = Background }
`)
	writeArchtestFile(t, root, "cmd/app/bad.go", `package main

import . "context"

func main() { _ = Background }
`)
	writeArchtestFile(t, root, "cmd/app/ok.go", `package main

import (
	ctx "context"
	_ "embed"
)

func ok() { _ = ctx.Background }
`)
	writeArchtestFile(t, root, "cmd/app/bad_test.go", `package main

import . "context"

func TestOnly(t *testing.T) { _ = Background }
`)

	violations, err := dotImportViolations(root)
	require.NoError(t, err)
	require.Len(t, violations, 2)
	assert.Contains(t, violations[0], "cmd/app/bad.go")
	assert.Contains(t, violations[0], `dot-imports "context"`)
	assert.Contains(t, violations[1], "root_bad.go")
}

func dotImportViolations(root string) ([]string, error) {
	var violations []string
	rootViolations, err := scanRootDotImports(root)
	if err != nil {
		return nil, err
	}
	violations = append(violations, rootViolations...)
	for _, dir := range dotImportProductionDirs {
		abs := filepath.Join(root, dir)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDotImportDir(path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fileViolations, scanErr := scanDotImports(root, path)
			if scanErr != nil {
				return scanErr
			}
			violations = append(violations, fileViolations...)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(violations)
	return violations, nil
}

func scanRootDotImports(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fileViolations, scanErr := scanDotImports(root, filepath.Join(root, name))
		if scanErr != nil {
			return nil, scanErr
		}
		violations = append(violations, fileViolations...)
	}
	return violations, nil
}

func skipDotImportDir(path, name string) bool {
	if name == "vendor" || name == "testdata" || name == "node_modules" {
		return true
	}
	return strings.HasPrefix(name, ".") && filepath.Base(path) != "."
}

func scanDotImports(root, path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, imp := range file.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		violations = append(violations, fmt.Sprintf("%s:%d dot-imports %s",
			filepath.ToSlash(rel), fset.Position(imp.Pos()).Line, imp.Path.Value))
	}
	return violations, nil
}

func writeArchtestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
