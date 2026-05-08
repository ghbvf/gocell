// Package fileutil exports file I/O helpers for GoCell tests.
//
// Centralizes the //nolint:gosec G304 suppression: tests routinely read
// files under t.TempDir() and committed fixtures, but gosec flags every
// os.ReadFile call with a non-literal path. The helper concentrates the
// suppression so individual call sites stay reason-free.
//
// Use MustReadFile / MustWriteFile when the test owns the path (constructed
// from t.TempDir, repo-relative join, known fixture, or scanner-discovered
// repo path). Do not use these from production code.
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
// Path safety contract: same as MustReadFile.
func MustWriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("fileutil: write %s: %v", path, err)
	}
}
