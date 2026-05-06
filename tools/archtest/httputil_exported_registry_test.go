// HTTPUTIL-SURFACE-REGISTERED-01 — every exported function in pkg/httputil
// must appear in at least one of the three authority tables:
//
//  1. pkg/httputil/doc.go Stable Surface comment (pattern: "  - FuncName")
//  2. kernel/governance/rules_http_response_alignment.go httpHelperWritesStatuses map
//  3. kernel/governance/rules_http_response_alignment.go knownNonWriters map (inline)
//
// This ensures that when a new exported function is added to pkg/httputil, the
// author is forced to register it in either the doc surface or the governance
// allowlist — preventing silent drift between the documented API surface and
// the actual exported surface.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHttputilExportedRegistry(t *testing.T) {
	t.Helper()

	// tools/archtest/ → ../../ = repo root.
	root, err := filepath.Abs("../../")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	httputil := filepath.Join(root, "pkg/httputil")
	docGoPath := filepath.Join(httputil, "doc.go")
	governancePath := filepath.Join(root, "kernel/governance/rules_http_response_alignment.go")

	// 1. Collect all exported functions from pkg/httputil (excluding test files).
	exported := collectExportedFuncs(t, httputil)

	// 2. Collect names registered in doc.go Stable Surface comment.
	docRegistered := collectDocRegistered(t, docGoPath)

	// 3. Collect names registered in governance maps (httpHelperWritesStatuses + knownNonWriters).
	governanceRegistered := collectGovernanceRegistered(t, governancePath)

	// 4. Assert every exported func is in at least one table.
	var missing []string
	for fn := range exported {
		inDoc := docRegistered[fn]
		inGov := governanceRegistered[fn]
		if !inDoc && !inGov {
			missing = append(missing, fn)
		}
	}
	if len(missing) > 0 {
		t.Errorf("HTTPUTIL-SURFACE-REGISTERED-01: the following exported pkg/httputil functions are not registered in doc.go Stable Surface OR kernel/governance maps — add them to pkg/httputil/doc.go and/or kernel/governance/rules_http_response_alignment.go: %v", missing)
	}
}

// collectExportedFuncs returns a set of top-level exported function names
// declared in non-test .go files under dir.
func collectExportedFuncs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	result := make(map[string]bool)
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil {
				// skip methods — only top-level functions
				continue
			}
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				result[name] = true
			}
		}
	}
	return result
}

// collectDocRegistered extracts function names from the Stable Surface section
// of doc.go. It matches lines of the form "  - FuncName" (with optional args).
func collectDocRegistered(t *testing.T, path string) map[string]bool {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		// Match "//   - FuncName" or "//   - FuncName(...)"
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "//")
		trimmed = strings.TrimSpace(trimmed)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "- ")
		// Function name is the identifier before '(' or space or end
		name := rest
		if idx := strings.IndexAny(rest, "( "); idx >= 0 {
			name = rest[:idx]
		}
		if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
			result[name] = true
		}
	}
	return result
}

// collectGovernanceRegistered extracts string literal keys from
// httpHelperWritesStatuses and knownNonWriters map literals in the governance
// file. Simple string-scanning approach (no full AST) — robust enough for
// stable map literals with one entry per line.
func collectGovernanceRegistered(t *testing.T, path string) map[string]bool {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		// Lines like: "WriteError": ... or "DecodeJSON": ...
		if !strings.HasPrefix(trimmed, `"`) {
			continue
		}
		end := strings.Index(trimmed[1:], `"`)
		if end < 0 {
			continue
		}
		key := trimmed[1 : end+1]
		if len(key) > 0 && key[0] >= 'A' && key[0] <= 'Z' {
			result[key] = true
		}
	}
	return result
}
