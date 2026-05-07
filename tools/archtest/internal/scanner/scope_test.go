package scanner_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// copyDir recursively copies src into dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("copyDir ReadDir %s: %v", src, err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				t.Fatalf("copyDir MkdirAll %s: %v", dstPath, err)
			}
			copyDir(t, srcPath, dstPath)
		} else {
			data, err := os.ReadFile(srcPath) //nolint:gosec // testdata path under test control
			if err != nil {
				t.Fatalf("copyDir ReadFile %s: %v", srcPath, err)
			}
			if err := os.WriteFile(dstPath, data, 0o644); err != nil { //nolint:gosec // temp dir under test control
				t.Fatalf("copyDir WriteFile %s: %v", dstPath, err)
			}
		}
	}
}

func scopeModuleFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	// Copy committed testdata (gofiles/, generated/, node_modules/, testdata/, vendor/).
	copyDir(t, "testdata/scope_module", tmp)
	// Rename gofiles/ → src/ to match canonical directory name the scanner tests expect.
	if err := os.Rename(filepath.Join(tmp, "gofiles"), filepath.Join(tmp, "src")); err != nil {
		t.Fatalf("rename gofiles→src: %v", err)
	}
	// Create gitignore-excluded directories that the scanner must skip.
	writeFile(t, filepath.Join(tmp, "worktrees", "w.go"), "package worktrees\n")
	writeFile(t, filepath.Join(tmp, "src", "a_test.go"), "package src\n")
	writeFile(t, filepath.Join(tmp, ".git", "keep"), "placeholder\n")
	return tmp
}

// writeFile creates parent dirs and writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeFile MkdirAll %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile WriteFile %s: %v", path, err)
	}
}

func TestModuleScope_SkipsBuiltInDirs(t *testing.T) {
	tmp := scopeModuleFixture(t)
	s := scanner.ModuleScope(tmp)
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	// Only src/a.go should be returned; _test.go excluded by default.
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(files), files)
	}
	rel, err := filepath.Rel(tmp, files[0])
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel != filepath.Join("src", "a.go") {
		t.Errorf("expected src/a.go, got %s", rel)
	}
}

func TestModuleScope_IncludeTests(t *testing.T) {
	tmp := scopeModuleFixture(t)
	s := scanner.ModuleScope(tmp, scanner.IncludeTests())
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	var rels []string
	for _, f := range files {
		rel, _ := filepath.Rel(tmp, f)
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	wantA := filepath.Join("src", "a.go")
	wantATest := filepath.Join("src", "a_test.go")
	found := map[string]bool{}
	for _, r := range rels {
		found[r] = true
	}
	if !found[wantA] {
		t.Errorf("missing %s in %v", wantA, rels)
	}
	if !found[wantATest] {
		t.Errorf("missing %s in %v", wantATest, rels)
	}
	if len(rels) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(rels), rels)
	}
}

func TestDirsScope_MissingDirReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	s := scanner.DirsScope(tmp, []string{"nonexistent"})
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty, got %v", files)
	}
}

func TestDirsScope_FiltersGoFiles(t *testing.T) {
	tmp := scopeModuleFixture(t)
	s := scanner.DirsScope(tmp, []string{"src"})
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(files), files)
	}
	rel, _ := filepath.Rel(tmp, files[0])
	if rel != filepath.Join("src", "a.go") {
		t.Errorf("expected src/a.go, got %s", rel)
	}
}

func TestExcludeRels_SelfExclusion(t *testing.T) {
	tmp := scopeModuleFixture(t)
	s := scanner.ModuleScope(tmp, scanner.ExcludeRels(filepath.Join("src", "a.go")))
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(tmp, f)
		if rel == filepath.Join("src", "a.go") {
			t.Errorf("excluded file %s was returned", rel)
		}
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files after exclusion, got %d: %v", len(files), files)
	}
}

func TestFiles_SortedAndDeduplicated(t *testing.T) {
	tmp := scopeModuleFixture(t)
	s := scanner.ModuleScope(tmp)
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	if !sort.StringsAreSorted(files) {
		t.Errorf("Files() not sorted: %v", files)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			t.Errorf("duplicate file: %s", f)
		}
		seen[f] = true
	}
}

func TestScope_ZeroValueIsRejected(t *testing.T) {
	var s scanner.Scope
	_, err := s.Files()
	if err == nil {
		t.Fatal("expected error from zero-value Scope, got nil")
	}
}

func TestScope_SelfProtectRel(t *testing.T) {
	// Create a temp tree that looks like the scanner package location.
	// ModuleScope must not include files under the self-protect path.
	tmp := t.TempDir()
	scannerDir := filepath.Join(tmp, "tools", "archtest", "internal", "scanner")
	if err := os.MkdirAll(scannerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	fakeFile := filepath.Join(scannerDir, "fake.go")
	if err := os.WriteFile(fakeFile, []byte("package scanner\n"), 0o644); err != nil {
		t.Fatalf("WriteFile fake.go: %v", err)
	}

	s := scanner.ModuleScope(tmp)
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	for _, f := range files {
		if f == fakeFile {
			t.Errorf("self-protect should exclude %s but it was returned", fakeFile)
		}
	}
}

func TestScope_SelfProtect_PathSegmentBoundary(t *testing.T) {
	// Self-protect must match path segments, not bare string prefixes.
	// scanner_extra/ shares the prefix tools/archtest/internal/scanner but
	// is a sibling directory; it must NOT be excluded.
	tmp := t.TempDir()
	scannerDir := filepath.Join(tmp, "tools", "archtest", "internal", "scanner")
	scannerExtraDir := filepath.Join(tmp, "tools", "archtest", "internal", "scanner_extra")
	if err := os.MkdirAll(scannerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll scanner: %v", err)
	}
	if err := os.MkdirAll(scannerExtraDir, 0o755); err != nil {
		t.Fatalf("MkdirAll scanner_extra: %v", err)
	}
	selfFile := filepath.Join(scannerDir, "self.go")
	siblingFile := filepath.Join(scannerExtraDir, "foo.go")
	if err := os.WriteFile(selfFile, []byte("package scanner\n"), 0o644); err != nil {
		t.Fatalf("WriteFile self.go: %v", err)
	}
	if err := os.WriteFile(siblingFile, []byte("package scanner_extra\n"), 0o644); err != nil {
		t.Fatalf("WriteFile foo.go: %v", err)
	}

	files, err := scanner.ModuleScope(tmp).Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	var seenSelf, seenSibling bool
	for _, f := range files {
		switch f {
		case selfFile:
			seenSelf = true
		case siblingFile:
			seenSibling = true
		}
	}
	if seenSelf {
		t.Errorf("self-protect should exclude %s", selfFile)
	}
	if !seenSibling {
		t.Errorf("self-protect must NOT exclude prefix-colliding sibling %s; got files=%v", siblingFile, files)
	}
}

func TestDirsScope_DeduplicatesOverlappingRoots(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "src", "a.go"), "package src\n")

	// Pass the same relative dir twice — Files() must deduplicate.
	s := scanner.DirsScope(tmp, []string{"src", "src"})
	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	// Single-root result must match two-root result.
	sSingle := scanner.DirsScope(tmp, []string{"src"})
	filesSingle, err := sSingle.Files()
	if err != nil {
		t.Fatalf("single DirsScope Files() error: %v", err)
	}
	if len(files) != len(filesSingle) {
		t.Errorf("DeduplicateOverlappingRoots: got %d files, single-root got %d", len(files), len(filesSingle))
	}
}

func TestDirsScope_EscapeReturnsError(t *testing.T) {
	tmp := t.TempDir()
	// Pass ".." which would escape modRoot.
	s := scanner.DirsScope(tmp, []string{".."})
	_, err := s.Files()
	if err == nil {
		t.Fatal("expected error for dir escaping module root, got nil")
	}
	if !containsAny(err.Error(), "escapes", "DirsScope") {
		t.Errorf("error message should mention escape: %v", err)
	}
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
