package initialadmin

import (
	"path/filepath"
	"testing"
)

// TestDefaultStateDir_NonEmptyAbsolute verifies that the platform-specific
// defaultStateDir() returns a non-empty absolute path on the current OS.
func TestDefaultStateDir_NonEmptyAbsolute(t *testing.T) {
	t.Parallel()

	dir, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: unexpected error: %v", err)
	}
	if dir == "" {
		t.Fatal("defaultStateDir: returned empty string")
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("defaultStateDir: expected absolute path, got %q", dir)
	}
}
