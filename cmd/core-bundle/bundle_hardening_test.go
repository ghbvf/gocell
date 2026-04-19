package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundle_NoBusinessPathLiterals locks in the F3 invariant: the composition
// roots (cmd/core-bundle/bundle.go and examples/sso-bff/main.go) must not
// hard-code cell-owned business paths like "POST /api/v1/access/sessions/login".
// Each route's Public / PasswordResetExempt / Delegated attributes are owned
// by the declaring Cell via auth.Declare; the composition root only supplies
// the auth-provider opt-in (bootstrap.WithAuthDiscovery).
//
// Regression surface: when a reviewer adds a WithPublicEndpoints shim or
// otherwise pastes a business path literal into a composition root, this test
// fails and points at the offending line.
func TestBundle_NoBusinessPathLiterals(t *testing.T) {
	// repoRoot resolves upward from the test file location to the repo root.
	// The test deliberately reads its own source files so a dev running the
	// test inside a worktree sees the same assertion as CI.
	repoRoot := findRepoRoot(t)

	cases := []struct {
		label string
		path  string
	}{
		{"cmd/core-bundle/bundle.go", filepath.Join(repoRoot, "cmd", "core-bundle", "bundle.go")},
		{"examples/sso-bff/main.go", filepath.Join(repoRoot, "examples", "sso-bff", "main.go")},
	}

	// Match any "METHOD /api/v1..." or "/api/v1..." literal. The former catches
	// WithPublicEndpoints-style string entries; the latter catches raw router
	// handler strings that bypass Cell ownership.
	methodLit := regexp.MustCompile(`"(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE)\s+/api/v1/`)
	rawLit := regexp.MustCompile(`"/api/v1/`)

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			body, err := os.ReadFile(tc.path)
			require.NoError(t, err, "read %s", tc.path)
			text := string(body)

			assert.False(t, methodLit.MatchString(text),
				"%s contains a \"METHOD /api/v1/...\" string literal — cells must own route declarations via auth.Declare",
				tc.label)
			assert.False(t, rawLit.MatchString(text),
				"%s contains a \"/api/v1/...\" string literal — cells must own route declarations via auth.Declare",
				tc.label)
		})
	}
}

// findRepoRoot walks upward from the working directory until it finds a
// go.mod file. The test was written as `go test ./cmd/core-bundle/...` so the
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
