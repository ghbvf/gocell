package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// receiverTypeName extracts the base type name from a receiver type expression.
// Handles *T (StarExpr), T (Ident), and T[P] (IndexExpr generic).
func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// findCellProductionGoFiles walks cells/** for production .go files
// (excluding _test.go + vendor + .git + testdata + generated). Used by both
// the outbox topic scanner (via packages.Load) and by AST-only scanners in
// repoerr_test.go that do not require type information.
func findCellProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", "generated", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// findArchTestDir returns the absolute path of the tools/archtest directory,
// used to locate testdata fixtures at test runtime.
func findArchTestDir(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	return filepath.Join(root, "tools", "archtest")
}
