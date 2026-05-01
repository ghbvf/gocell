package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundle_NoBusinessPathLiterals locks in the F3 invariant: the composition
// roots (cmd/*/bundle.go, cmd/*/main.go, examples/*/main.go) must not
// hard-code cell-owned business paths like "POST /api/v1/access/sessions/login".
// Each route's Public / PasswordResetExempt / IsInternal() attributes are owned
// by the declaring Cell via auth.Mount; the composition root only wires the
// listener policy (PolicyJWTFromAssembly on PrimaryListener post-F3 round-3).
//
// Using filepath.Glob to discover files means new cmd/ or examples/ composition
// roots are automatically covered without editing this test.
//
// Regression surface: when a reviewer adds a WithPublicEndpoints shim or
// otherwise pastes a business path literal into a composition root, this test
// fails and points at the offending file + line number.
func TestBundle_NoBusinessPathLiterals(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Collect all production composition-root Go files.
	// Test files (*_test.go) are excluded: assertion strings in tests are fine.
	var candidates []string
	for _, pattern := range []string{
		filepath.Join(repoRoot, "cmd", "*", "*.go"),
		filepath.Join(repoRoot, "examples", "*", "main.go"),
		filepath.Join(repoRoot, "examples", "*", "bundle.go"),
	} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err, "glob %s", pattern)
		for _, m := range matches {
			if !strings.HasSuffix(m, "_test.go") {
				candidates = append(candidates, m)
			}
		}
	}
	require.NotEmpty(t, candidates, "no composition-root files found — glob patterns may need updating")

	// Match any "METHOD /api/v1..." or "/api/v1..." literal. The former catches
	// WithPublicEndpoints-style string entries; the latter catches raw router
	// handler strings that bypass Cell ownership.
	methodLit := regexp.MustCompile(`"(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE)\s+/api/v1/`)
	rawLit := regexp.MustCompile(`"/api/v1/`)

	for _, filePath := range candidates {
		rel, err := filepath.Rel(repoRoot, filePath)
		if err != nil {
			rel = filePath
		}
		t.Run(rel, func(t *testing.T) {
			offenses := findOffendingLines(t, filePath, methodLit, rawLit)
			assert.Empty(t, offenses,
				"%s contains path literals that must be owned by Cells via auth.Mount:\n%s",
				rel, strings.Join(offenses, "\n"))
		})
	}
}

// findOffendingLines returns a slice of "line N: <content>" strings for every
// line in filePath that matches either pattern. Returns nil when the file is
// clean.
func findOffendingLines(t *testing.T, filePath string, patterns ...*regexp.Regexp) []string {
	t.Helper()
	f, err := os.Open(filepath.Clean(filePath))
	require.NoError(t, err, "open %s", filePath)
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Logf("close file: %v", err)
		}
	})

	var hits []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, pat := range patterns {
			if pat.MatchString(line) {
				hits = append(hits, fmt.Sprintf("  line %d: %s", lineNum, strings.TrimSpace(line)))
				break // one hit per line is enough
			}
		}
	}
	require.NoError(t, scanner.Err(), "scan %s", filePath)
	return hits
}

// findRepoRoot walks upward from the working directory until it finds a
// go.mod file. The test was written as `go test ./cmd/corebundle/...` so the
// working directory is the package dir; repo root is three levels up, but we
// do the walk defensively in case the harness changes.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	for dir := wd; dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	t.Fatalf("could not locate go.mod from %s", wd)
	return ""
}
