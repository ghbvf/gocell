package prodscan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPatternsSkipModuleRootsButWorkspacePatternsKeepThem(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "kernel"))
	mustMkdir(t, filepath.Join(root, "corecells"))
	mustWrite(t, filepath.Join(root, "corecells", "go.mod"), "module example.com/root/corecells\n")

	rootOnly := PatternTopLevels(Patterns(root))
	if rootOnly["corecells"] {
		t.Fatal("Patterns must skip module roots under ModeModule")
	}
	if !rootOnly["kernel"] {
		t.Fatal("Patterns must keep ordinary production dirs")
	}

	workspace := PatternTopLevels(WorkspacePatterns(root))
	if !workspace["corecells"] {
		t.Fatal("WorkspacePatterns must include workspace module roots")
	}
	if !workspace["kernel"] {
		t.Fatal("WorkspacePatterns must keep ordinary production dirs")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
