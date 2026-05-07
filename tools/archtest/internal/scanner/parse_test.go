package scanner

import (
	"go/parser"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFile_ErrorFailLoud(t *testing.T) {
	// Copy broken.go.txt → tmp/broken.go, then call eachFile and expect error.
	tmp := t.TempDir()
	src := filepath.Join("testdata", "parse_broken", "broken.go.txt")
	data, err := os.ReadFile(src) //nolint:gosec // testdata path under test control
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	dst := filepath.Join(tmp, "broken.go")
	if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // temp dir under test control
		t.Fatalf("WriteFile: %v", err)
	}

	s := DirsScope(tmp, []string{"."})
	err = eachFile(s, parser.ImportsOnly, func(fc FileContext) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should contain 'parse', got: %v", err)
	}
	if !strings.Contains(err.Error(), dst) {
		t.Errorf("error should contain file path %s, got: %v", dst, err)
	}
}

func TestParseFile_OkFileSucceeds(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join("testdata", "parse_broken", "ok.go.txt")
	data, err := os.ReadFile(src) //nolint:gosec // testdata path under test control
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	dst := filepath.Join(tmp, "ok.go")
	if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // temp dir under test control
		t.Fatalf("WriteFile: %v", err)
	}

	s := DirsScope(tmp, []string{"."})
	var visited []string
	err = eachFile(s, parser.ImportsOnly, func(fc FileContext) error {
		visited = append(visited, fc.Rel)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(visited) != 1 {
		t.Errorf("expected 1 file, got %d: %v", len(visited), visited)
	}
}

func TestParseFile_ImportsOnlyMode(t *testing.T) {
	tmp := t.TempDir()
	// Write a file with a function body — ImportsOnly should still succeed.
	content := []byte("package foo\nimport \"fmt\"\nfunc F() { fmt.Println() }\n")
	dst := filepath.Join(tmp, "f.go")
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := DirsScope(tmp, []string{"."})
	var gotImports []string
	err := eachFile(s, parser.ImportsOnly, func(fc FileContext) error {
		for _, imp := range fc.File.Imports {
			gotImports = append(gotImports, imp.Path.Value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotImports) != 1 || gotImports[0] != `"fmt"` {
		t.Errorf("expected [\"fmt\"], got %v", gotImports)
	}
}
