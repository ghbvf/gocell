// INVARIANT: ACCESSCORE-FACADE-A61-01: accesscore must not grow a thin top-level credential-path forwarding API
package archtest

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestAccessCoreFacadePolishA61Guard locks A61's no-shim decision: the
// initialadmin credential-path resolver is owned by the initialadmin package,
// and accesscore must not grow a thin top-level forwarding API again.
func TestAccessCoreFacadePolishA61Guard(t *testing.T) {
	root := findModuleRoot(t)

	t.Run("R9_no_bootstrap_credential_path_facade", func(t *testing.T) {
		var violations []string

		violations = append(violations, scanAccesscoreFacadeDeclarations(t, root)...)

		for _, dir := range []string{"cmd", "examples"} {
			violations = append(violations, scanResolveBootstrapCredentialPathCalls(t, root, filepath.Join(root, dir))...)
		}

		assert.Empty(t, violations, strings.Join(violations, "\n"))
	})
}

func scanAccesscoreFacadeDeclarations(t *testing.T, root string) []string {
	t.Helper()
	var violations []string
	scope := scanner.DirsScope(root, []string{"cells/accesscore"})
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		scanner.EachInSubtree[ast.FuncDecl](fc.File, func(fn *ast.FuncDecl) {
			if fn.Recv == nil && fn.Name.Name == "ResolveBootstrapCredentialPath" {
				violations = append(violations, fc.Rel+": exported facade ResolveBootstrapCredentialPath must be deleted")
			}
		})
	})
	return violations
}

func scanResolveBootstrapCredentialPathCalls(t *testing.T, root, dir string) []string {
	t.Helper()
	rel, err := filepath.Rel(root, dir)
	require.NoError(t, err)
	scope := scanner.DirsScope(root, []string{filepath.ToSlash(rel)})
	var violations []string
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		scanner.EachInSubtree[ast.SelectorExpr](fc.File, func(sel *ast.SelectorExpr) {
			if sel.Sel.Name != "ResolveBootstrapCredentialPath" {
				return
			}
			pos := fc.Fset.Position(sel.Sel.Pos())
			violations = append(violations,
				fc.Rel+":"+strconv.Itoa(pos.Line)+": call initialadmin.ResolveCredentialPath directly")
		})
	})
	return violations
}
