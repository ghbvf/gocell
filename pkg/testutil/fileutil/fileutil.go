// Package fileutil exports file I/O helpers for GoCell tests.
//
// Centralizes the //nolint:gosec G304 suppression: tests routinely read
// files under t.TempDir() and committed fixtures, but gosec flags every
// os.ReadFile call with a non-literal path. The helper concentrates the
// suppression so individual call sites stay reason-free.
//
// Use MustReadFile / MustWriteFile when the test owns the path (constructed
// from t.TempDir, repo-relative join, known fixture, or scanner-discovered
// repo path). Do not use these from production code, and do not use them
// when the path originates from external input (env var, CLI arg, HTTP
// query) even in test code — those sites must call os.ReadFile directly
// with an inline lint suppression that documents the input's safety
// argument.
package fileutil

import (
	"os"
	"testing"
)

// MustReadFile reads path and returns its contents, calling t.Fatalf on error.
//
// Path safety contract: the caller is a test and constructed path itself
// (t.TempDir, repo-relative join, known fixture, or archtest scanner output) —
// not user-controlled input. Production code must not call this helper.
func MustReadFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is test-controlled (see package godoc)
	if err != nil {
		t.Fatalf("fileutil: read %s: %v", path, err)
	}
	return b
}

// MustWriteFile writes data to path with mode 0o600, calling t.Fatalf on error.
//
// Path safety contract: same as MustReadFile.
//
// Mode rationale: 0o600 (owner read-write only) is sufficient because tests
// run as the same user that reads the file back; group/other permissions are
// not needed and the tighter mode also passes gosec G306 without further
// suppression. Migrate 0o644 sites to this helper rather than relaxing the
// helper's mode.
func MustWriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("fileutil: write %s: %v", path, err)
	}
}
