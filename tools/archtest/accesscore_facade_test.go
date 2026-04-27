package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccessCoreFacadePolishA61Guard locks A61's no-shim decision: the
// initialadmin credential-path resolver is owned by the initialadmin package,
// and accesscore must not grow a thin top-level forwarding API again.
func TestAccessCoreFacadePolishA61Guard(t *testing.T) {
	root := findModuleRoot(t)
	fset := token.NewFileSet()

	t.Run("R9_no_bootstrap_credential_path_facade", func(t *testing.T) {
		var violations []string

		accesscoreCell := filepath.Join(root, "cells", "accesscore", "cell.go")
		file, err := parser.ParseFile(fset, accesscoreCell, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "ResolveBootstrapCredentialPath" {
				violations = append(violations, relPath(t, root, accesscoreCell)+": exported facade ResolveBootstrapCredentialPath must be deleted")
			}
		}

		for _, dir := range []string{"cmd", "examples"} {
			violations = append(violations, scanResolveBootstrapCredentialPathCalls(t, fset, root, filepath.Join(root, dir))...)
		}

		assert.Empty(t, violations, strings.Join(violations, "\n"))
	})
}

func scanResolveBootstrapCredentialPathCalls(t *testing.T, fset *token.FileSet, root, dir string) []string {
	t.Helper()
	var violations []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "generated", "testdata", "vendor", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ResolveBootstrapCredentialPath" {
				return true
			}
			pos := fset.Position(sel.Sel.Pos())
			violations = append(violations,
				relPath(t, root, path)+":"+strconv.Itoa(pos.Line)+": call initialadmin.ResolveCredentialPath directly")
			return true
		})
		return nil
	})
	require.NoError(t, err)
	return violations
}

func relPath(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	require.NoError(t, err)
	return filepath.ToSlash(rel)
}
