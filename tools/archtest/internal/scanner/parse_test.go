package scanner_test

import (
	"go/parser"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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

	s := scanner.DirsScope(tmp, []string{"."})
	err = scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should contain 'parse', got: %v", err)
	}
	// Error message uses the module-relative path (not the absolute path).
	const wantRelPath = "broken.go"
	if !strings.Contains(err.Error(), wantRelPath) {
		t.Errorf("error should contain relative path %q, got: %v", wantRelPath, err)
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

	s := scanner.DirsScope(tmp, []string{"."})
	var visited []string
	err = scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
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

	s := scanner.DirsScope(tmp, []string{"."})
	var gotImports []string
	err := scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
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

func TestParseFile_ImportsOnly_Mode_Assertions(t *testing.T) {
	// C5 finding: assert fc.Rel is slash-separated, fc.AbsPath non-empty,
	// fc.File and fc.Fset non-nil.
	tmp := t.TempDir()
	content := []byte("package bar\nimport \"os\"\n")
	if err := os.WriteFile(filepath.Join(tmp, "bar.go"), content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"."})
	err := scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		if strings.Contains(fc.Rel, "\\") {
			t.Errorf("fc.Rel should be slash-separated, got %q", fc.Rel)
		}
		if fc.AbsPath == "" {
			t.Error("fc.AbsPath should be non-empty")
		}
		if fc.File == nil {
			t.Error("fc.File should be non-nil")
		}
		if fc.Fset == nil {
			t.Error("fc.Fset should be non-nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
