package archtest

// listener_dx_test.go enforces A52 LISTENER-DX-UNIFY anti-regression guards.
//
// The rule is intentionally narrow:
//   - production Go must not reintroduce deleted listener option APIs;
//   - production Go must not reintroduce the old auth.Route Delegated field;
//   - active docs/godoc must not show old listener APIs or Delegated examples.
//
// Historical provenance remains allowed in docs/backlog.md, docs/plans/**,
// docs/reviews/**, docs/archive/**, and CHANGELOG.md.

import (
	"fmt"
	"go/ast"
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

const ruleListenerDXA52 = "LISTENER-DX-A52"

var oldListenerAPIIdents = map[string]struct{}{
	"WithHTTPAddr":         {},
	"WithHTTPPrimaryAddr":  {},
	"WithHTTPInternalAddr": {},
	"WithPrimaryListener":  {},
	"WithInternalListener": {},
}

var activeDocForbiddenTerms = []string{
	"WithHTTPAddr",
	"WithHTTPPrimaryAddr",
	"WithHTTPInternalAddr",
	"WithPrimaryListener",
	"WithInternalListener",
	"Delegated",
}

func TestListenerDXA52Guard(t *testing.T) {
	root := findModuleRoot(t)

	t.Run("deleted_listener_options_not_reintroduced", func(t *testing.T) {
		files, err := listenerDXProductionGoFiles(root)
		require.NoError(t, err)
		var violations []string
		for _, file := range files {
			violations = append(violations, oldListenerAPIIdentViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: deleted listener option APIs must not reappear:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("delegated_route_field_not_reintroduced", func(t *testing.T) {
		files, err := listenerDXProductionGoFiles(root)
		require.NoError(t, err)
		var violations []string
		for _, file := range files {
			violations = append(violations, delegatedRouteFieldViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: auth.Route Delegated field must not reappear:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("active_docs_do_not_show_deleted_listener_surface", func(t *testing.T) {
		files, err := listenerDXActiveDocs(root)
		require.NoError(t, err)
		var violations []string
		for _, file := range files {
			violations = append(violations, activeDocTermViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: active docs/godoc must not show deleted listener APIs or Delegated examples:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})
}

func listenerDXProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "generated", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func listenerDXActiveDocs(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if listenerDXDocExcluded(rel) {
			return nil
		}
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, "/doc.go") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func listenerDXDocExcluded(rel string) bool {
	if rel == "CHANGELOG.md" || rel == "docs/backlog.md" {
		return true
	}
	for _, prefix := range []string{
		"docs/plans/",
		"docs/reviews/",
		"docs/archive/",
	} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func oldListenerAPIIdentViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if _, forbidden := oldListenerAPIIdents[id.Name]; !forbidden {
			return true
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(id.Pos()).Line,
			fmt.Sprintf("deleted listener option identifier %q", id.Name)))
		return true
	})
	return violations
}

func delegatedRouteFieldViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Delegated" {
			return true
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(key.Pos()).Line,
			"Delegated key in composite literal"))
		return true
	})
	return violations
}

func activeDocTermViolations(t *testing.T, root, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	var violations []string
	for i, line := range lines {
		for _, term := range activeDocForbiddenTerms {
			if strings.Contains(line, term) {
				violations = append(violations, listenerDXViolation(root, path, i+1,
					fmt.Sprintf("active docs/godoc contains %q", term)))
			}
		}
	}
	return violations
}

func listenerDXViolation(root, path string, line int, msg string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	return fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), line, msg)
}
