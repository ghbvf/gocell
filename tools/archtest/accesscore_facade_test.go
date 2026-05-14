// INVARIANT: ACCESSCORE-FACADE-A61-01: accesscore must not grow a thin top-level credential-path forwarding API
package archtest

import (
	"go/ast"
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
	scope := DirsScope(root, []string{"cells/accesscore"})
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Recv == nil && fn.Name.Name == "ResolveBootstrapCredentialPath" {
					violations = append(violations, p.Rel(file)+": exported facade ResolveBootstrapCredentialPath must be deleted")
				}
			})
		}
		return nil
	})
	return violations
}

func scanResolveBootstrapCredentialPathCalls(t *testing.T, root, dir string) []string {
	t.Helper()
	rel, err := filepath.Rel(root, dir)
	require.NoError(t, err)
	scope := DirsScope(root, []string{filepath.ToSlash(rel)})
	var violations []string
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel.Name != "ResolveBootstrapCredentialPath" {
					return
				}
				pos := p.Fset.Position(sel.Sel.Pos())
				violations = append(violations,
					p.Rel(file)+":"+strconv.Itoa(pos.Line)+": call initialadmin.ResolveCredentialPath directly")
			})
		}
		return nil
	})
	return violations
}
