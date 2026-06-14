package scanner_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
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
			data := fileutil.MustReadFile(t, srcPath)
			fileutil.MustWriteFile(t, dstPath, data)
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

func TestScope_ExcludesArchtestInternalTree(t *testing.T) {
	// A repo-rooted ModuleScope walk must exclude every file under
	// tools/archtest/internal/ — the scanner walker itself plus all RED
	// fixtures and typed-load helpers. archtest's internal tree is never a
	// production-governance target (see archtestInternalRel in scope.go).
	tmp := t.TempDir()
	internalFiles := []string{
		filepath.Join(tmp, "tools", "archtest", "internal", "scanner", "fake.go"),
		filepath.Join(tmp, "tools", "archtest", "internal", "somefixture", "red.go"),
		filepath.Join(tmp, "tools", "archtest", "internal", "typeseval", "helper.go"),
	}
	for _, f := range internalFiles {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", f, err)
		}
		if err := os.WriteFile(f, []byte("package p\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	files, err := scanner.ModuleScope(tmp).Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	for _, f := range files {
		for _, excluded := range internalFiles {
			if f == excluded {
				t.Errorf("archtest internal tree must be excluded, but %s was returned", excluded)
			}
		}
	}
}

func TestScope_ArchtestInternalExclusion_PathSegmentBoundary(t *testing.T) {
	// The exclusion must match path segments, not bare string prefixes.
	// internalx/ collides on the "internal" prefix but is a sibling directory
	// (not under tools/archtest/internal/); it must NOT be excluded.
	tmp := t.TempDir()
	internalDir := filepath.Join(tmp, "tools", "archtest", "internal", "scanner")
	siblingDir := filepath.Join(tmp, "tools", "archtest", "internalx")
	if err := os.MkdirAll(internalDir, 0o755); err != nil {
		t.Fatalf("MkdirAll internal: %v", err)
	}
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll internalx: %v", err)
	}
	selfFile := filepath.Join(internalDir, "self.go")
	siblingFile := filepath.Join(siblingDir, "foo.go")
	if err := os.WriteFile(selfFile, []byte("package scanner\n"), 0o644); err != nil {
		t.Fatalf("WriteFile self.go: %v", err)
	}
	if err := os.WriteFile(siblingFile, []byte("package internalx\n"), 0o644); err != nil {
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
		t.Errorf("archtest internal tree must exclude %s", selfFile)
	}
	if !seenSibling {
		t.Errorf("exclusion must NOT match prefix-colliding sibling %s; got files=%v", siblingFile, files)
	}
}

func TestScope_ExcludesArchtestInternalTree_ContentFiles(t *testing.T) {
	// collectFile is the shared backbone of Files() (.go) and contentFiles()
	// (YAML/SQL/MD via LoadContentFiles/EachContentFile), so the archtest-internal
	// exclusion must apply to non-Go content files too — not just .go.
	tmp := t.TempDir()
	internalYAML := filepath.Join(tmp, "tools", "archtest", "internal", "somefixture", "fixture.yaml")
	controlYAML := filepath.Join(tmp, "cells", "auth", "cell.yaml")
	for _, f := range []string{internalYAML, controlYAML} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", f, err)
		}
		if err := os.WriteFile(f, []byte("id: x\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	got, err := scanner.LoadContentFiles(scanner.ModuleScope(tmp), []string{".yaml"})
	if err != nil {
		t.Fatalf("LoadContentFiles error: %v", err)
	}
	var sawInternal, sawControl bool
	for _, cc := range got {
		switch cc.AbsPath {
		case internalYAML:
			sawInternal = true
		case controlYAML:
			sawControl = true
		}
	}
	if sawInternal {
		t.Errorf("archtest internal tree must be excluded from content files, but %s was returned", internalYAML)
	}
	if !sawControl {
		t.Errorf("control content file %s must be returned (exclusion must be specific to internal/); got=%v", controlYAML, got)
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
	var escapeErr *scanner.DirsScopeEscapeError
	if !errors.As(err, &escapeErr) {
		t.Fatalf("expected *DirsScopeEscapeError, got %T: %v", err, err)
	}
	if len(escapeErr.Dirs) != 1 || escapeErr.Dirs[0] != ".." {
		t.Errorf("expected Dirs=[..], got %v", escapeErr.Dirs)
	}
}

// TestDirsScope_RootInsideSkippedAncestorIsAllowed locks in the boundary
// behavior exercised by TestCodegenContractUserOverlap01: even though
// "generated" is in defaultSkipDirs, a DirsScope rooted at "generated/contracts"
// must still walk normally because skipDirs is consulted on directories
// encountered DURING the walk (matched by base name), not on the supplied root.
func TestDirsScope_RootInsideSkippedAncestorIsAllowed(t *testing.T) {
	tmp := t.TempDir()
	rootDir := filepath.Join(tmp, "generated", "contracts")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := filepath.Join(rootDir, "x_gen.go")
	if err := os.WriteFile(want, []byte("package contracts\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	files, err := scanner.DirsScope(tmp, []string{"generated/contracts"}).Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	if len(files) != 1 || files[0] != want {
		t.Errorf("expected [%s], got %v", want, files)
	}
}

// TestDirsScope_EscapeErrorListsAllOutOfBoundPaths verifies the docstring
// promise on DirsScope ("returns an error listing every out-of-bound path"):
// when multiple dirs escape modRoot, the structured error must enumerate ALL
// offenders in Dirs, not just the first.
func TestDirsScope_EscapeErrorListsAllOutOfBoundPaths(t *testing.T) {
	tmp := t.TempDir()
	s := scanner.DirsScope(tmp, []string{"../sibling-a", "../sibling-b", "valid"})
	_, err := s.Files()
	if err == nil {
		t.Fatal("expected error for dirs escaping module root, got nil")
	}
	var escapeErr *scanner.DirsScopeEscapeError
	if !errors.As(err, &escapeErr) {
		t.Fatalf("expected *DirsScopeEscapeError, got %T: %v", err, err)
	}
	wantDirs := []string{"../sibling-a", "../sibling-b"}
	sort.Strings(escapeErr.Dirs)
	sort.Strings(wantDirs)
	if !reflect.DeepEqual(escapeErr.Dirs, wantDirs) {
		t.Errorf("expected Dirs=%v, got %v", wantDirs, escapeErr.Dirs)
	}
}
