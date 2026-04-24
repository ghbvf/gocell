//go:build linux

package initialadmin

import (
	"strings"
	"testing"
)

func TestDefaultStateDir_LinuxPrefix(t *testing.T) {
	t.Parallel()

	dir, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	if !strings.HasPrefix(dir, "/run/gocell") {
		t.Errorf("expected /run/gocell prefix, got %q", dir)
	}
}
