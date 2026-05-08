package scanner_test

import (
	"errors"
	"go/parser"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestEachFile_DefaultExcludesTests(t *testing.T) {
	tmp := t.TempDir()
	// Create src/a.go and src/a_test.go
	if err := os.MkdirAll(filepath.Join(tmp, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "a_test.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a_test.go: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"src"})
	var rels []string
	err := scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		rels = append(rels, fc.Rel)
		return nil
	})
	if err != nil {
		t.Fatalf("eachFile error: %v", err)
	}
	for _, r := range rels {
		if strings.HasSuffix(r, "_test.go") {
			t.Errorf("_test.go file should be excluded by default: %s", r)
		}
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 file, got %d: %v", len(rels), rels)
	}
}

func TestEachFile_IncludeTests(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "a_test.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a_test.go: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"src"}, scanner.IncludeTests())
	var rels []string
	err := scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		rels = append(rels, fc.Rel)
		return nil
	})
	if err != nil {
		t.Fatalf("eachFile error: %v", err)
	}
	hasTest := false
	for _, r := range rels {
		if strings.HasSuffix(r, "_test.go") {
			hasTest = true
		}
	}
	if !hasTest {
		t.Errorf("expected _test.go file when IncludeTests(), got: %v", rels)
	}
	if len(rels) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(rels), rels)
	}
}

func TestEachFile_ParseErrorIsPropagated(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join("testdata", "parse_broken", "broken.go.txt")
	data, err := os.ReadFile(src) //nolint:gosec // testdata path under test control
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "broken.go"), data, 0o644); err != nil { //nolint:gosec // temp dir under test control
		t.Fatalf("WriteFile: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"."})
	err = scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error from broken.go, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse': %v", err)
	}
}

func TestEachFile_FnErrorIsPropagated(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"."})
	sentinel := errors.New("fn error sentinel")
	err := scanner.EachFileInternal(s, parser.ImportsOnly, func(fc scanner.FileContext) error {
		return sentinel
	})
	if err == nil {
		t.Fatal("expected fn error to propagate")
	}
}

func TestEachFile_ExportedWrapper_Success(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := scanner.DirsScope(tmp, []string{"."})
	var visited []string
	scanner.EachFile(t, s, parser.ImportsOnly, func(tt *testing.T, fc scanner.FileContext) {
		visited = append(visited, fc.Rel)
	})
	if len(visited) != 1 {
		t.Errorf("expected 1 file via EachFile, got %d: %v", len(visited), visited)
	}
}
