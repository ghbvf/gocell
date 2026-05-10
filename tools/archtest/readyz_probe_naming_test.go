package archtest

// INVARIANT: READYZ-PROBE-NAMING-01
//
// readyz_probe_naming_test.go — READYZ-PROBE-NAMING-01
//
// Rule: health probe names registered via reg.Health(name, fn) in cells/ and
// adapters/ production files must use snake_case only — no hyphens. Names that
// are dependency-availability probes must end with the _ready suffix (e.g.
// rabbitmq_ready, postgres_ready, session_store_ready). This makes probe names
// stable operability contracts visible in /readyz verbose output.
//
// Enforcement scope: cells/ and adapters/ production Go files (.go, not _test.go).
// Bootstrap internal probes (config_watcher, config_drift, event_router) are
// registered via bootstrap constants, not via reg.Health calls, so they are
// outside this scan scope.
//
// Detection: AST-based scan for CallExpr where the method selector is "Health"
// and the first argument is a string literal containing a hyphen.
//
// ref: .claude/rules/gocell/observability.md — Readyz Probe 命名
// ref: kernel/lifecycle/doc.go — "_ready" suffix convention

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const readyzProbeNaming01 = "READYZ-PROBE-NAMING-01"

// TestReadyzProbeNaming scans cells/ and adapters/ for reg.Health(name, fn)
// calls where the probe name contains a hyphen.
func TestReadyzProbeNaming(t *testing.T) {
	root := findModuleRoot(t)

	var violations []string

	scanDirs := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "adapters"),
	}

	for _, dir := range scanDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		files, err := findProductionGoFilesInDir(dir)
		require.NoErrorf(t, err, "reading %s", dir)

		for _, f := range files {
			hits, err := findHyphenatedHealthProbeNames(f)
			require.NoErrorf(t, err, "scanning %s", f)
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			for _, hit := range hits {
				violations = append(violations,
					fmt.Sprintf("%s:%d: probe name %q contains hyphen — use snake_case with _ready suffix (%s)",
						rel, hit.line, hit.name, readyzProbeNaming01))
			}
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", readyzProbeNaming01, v)
		}
	}
	assert.Empty(t, violations,
		"all reg.Health probe names in cells/ and adapters/ must be snake_case (no hyphens); "+
			"dependency probes must end with _ready (e.g. session_store_ready, rabbitmq_ready)")
}

// probeNameHit records a probe name and its source line.
type probeNameHit struct {
	name string
	line int
}

// findHyphenatedHealthProbeNames parses path and returns every .Health(name, fn)
// call where name is a string literal containing a hyphen.
func findHyphenatedHealthProbeNames(path string) ([]probeNameHit, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var hits []probeNameHit
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Match any selector call ending in .Health(...)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Health" {
			return true
		}
		// First argument must be a string literal.
		if len(call.Args) < 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind.String() != "STRING" {
			return true
		}
		// Unquote: remove surrounding double quotes.
		name := strings.Trim(lit.Value, `"`)
		if strings.Contains(name, "-") {
			hits = append(hits, probeNameHit{
				name: name,
				line: fset.Position(call.Pos()).Line,
			})
		}
		return true
	})
	return hits, nil
}

// TestReadyzProbeNaming_Fixture_CatchesHyphenatedName verifies that
// findHyphenatedHealthProbeNames correctly detects "session-store" as a
// violation and passes "session_store_ready".
func TestReadyzProbeNaming_Fixture_CatchesHyphenatedName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cell_init.go")
	src := `package accesscore

import "context"

type reg interface {
	Health(name string, fn func(context.Context) error)
}

func register(r reg) {
	r.Health("session-store", nil)       // violation: hyphen
	r.Health("session_store_ready", nil) // ok: snake_case + _ready suffix
}
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	hits, err := findHyphenatedHealthProbeNames(path)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "session-store", hits[0].name)
}
