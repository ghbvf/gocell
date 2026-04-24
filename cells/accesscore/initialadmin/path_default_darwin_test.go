//go:build darwin

package initialadmin

import (
	"strings"
	"testing"
)

func TestDefaultStateDir_DarwinPrefix(t *testing.T) {
	t.Parallel()

	dir, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	if !strings.Contains(dir, "Library/Application Support/gocell/run") {
		t.Errorf("expected Library/Application Support/gocell/run in path, got %q", dir)
	}
}
