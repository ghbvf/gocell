//go:build windows

package initialadmin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultStateDir_WindowsContainsGocell(t *testing.T) {
	t.Parallel()

	dir, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	if !strings.Contains(dir, "gocell") {
		t.Errorf("expected 'gocell' in path, got %q", dir)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("expected absolute path, got %q", dir)
	}
}

func TestDefaultStateDir_Windows_HonorsLocalAppData(t *testing.T) {
	customDir := `C:\custom\appdata`
	t.Setenv("LOCALAPPDATA", customDir)

	dir, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	want := filepath.Join(customDir, "gocell", "run")
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}
