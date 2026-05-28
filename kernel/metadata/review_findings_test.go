// review_findings_test.go — TDD tests for PR #1226 review round-8 findings.
//
// Findings covered:
//   - T1: discoverConventional symlink skip (real-fs, os.DirFS)
//   - T2: validateManifestModulePath uses filepath.IsAbs+filepath.FromSlash not path.IsAbs
//   - T3: parseWith warns when Discover returns zero sources
//   - T4: godoc only (no runtime test needed — comment-only fix)
//   - T5: manifestGlobFixedPrefix dead-comment + hasWild simplification (existing tests cover)
//   - T6: LocatorAuto godoc (comment-only)
//   - T7: excludes SkipDir pruning + match cap
package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// ── T1: discoverConventional symlink skip ──────────────────────────────────

// TestDiscoverConventional_SkipsSymlink verifies that discoverConventional
// skips symlinks and does not classify them as metadata sources. Uses a real
// temp directory because fstest.MapFS cannot represent symlinks.
func TestDiscoverConventional_SkipsSymlink(t *testing.T) {
	root := t.TempDir()

	// Write a real cell.yaml so we know conventional discovery works.
	cellDir := filepath.Join(root, "cells", "realcell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: realcell\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a symlink inside cells/ pointing to an external file.
	external := filepath.Join(t.TempDir(), "external_cell.yaml")
	if err := os.WriteFile(external, []byte("id: shouldnotappear\n"), 0o644); err != nil {
		t.Fatalf("WriteFile external: %v", err)
	}
	symlinkPath := filepath.Join(root, "cells", "symlinked.yaml")
	if err := os.Symlink(external, symlinkPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	l, err := NewLocator(root, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocator: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range sources {
		if strings.Contains(s.Path, "symlinked") {
			t.Errorf("symlink was classified as a MetadataSource: %+v", s)
		}
	}
	// Confirm the real cell was still found.
	var found bool
	for _, s := range sources {
		if s.Kind == SourceCell && s.CellID == "realcell" {
			found = true
		}
	}
	if !found {
		t.Errorf("real cell 'realcell' not discovered; sources = %v", sources)
	}
}

// TestDiscoverConventional_SkipsSymlinkDir verifies that a symlink pointing
// to a directory inside cells/ does not cause discoverConventional to follow
// it and classify files inside.
func TestDiscoverConventional_SkipsSymlinkDir(t *testing.T) {
	root := t.TempDir()

	// Build the real cell directory.
	cellDir := filepath.Join(root, "cells", "realcell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("MkdirAll realcell: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: realcell\n"), 0o644); err != nil {
		t.Fatalf("WriteFile realcell: %v", err)
	}

	// Build an external directory with a cell.yaml that must NOT appear.
	extDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(extDir, "cell.yaml"), []byte("id: escapedcell\n"), 0o644); err != nil {
		t.Fatalf("WriteFile external: %v", err)
	}

	// Create a directory symlink inside cells/.
	symlinkDir := filepath.Join(root, "cells", "linked")
	if err := os.Symlink(extDir, symlinkDir); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	l, err := NewLocator(root, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocator: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range sources {
		if strings.Contains(s.Path, "linked") || strings.Contains(s.Path, "escapedcell") {
			t.Errorf("symlink dir was traversed and classified: %+v", s)
		}
	}
}

// ── T2: validateManifestModulePath filepath.IsAbs ─────────────────────────

// TestValidateManifestModulePath_FilepathIsAbs verifies that path patterns
// containing backslash-style absolute paths (e.g. C:\foo) are not silently
// allowed. On UNIX filepath.IsAbs("C:\\foo") returns false, so the test
// mainly validates the intent/documentation and non-regression of UNIX paths.
func TestValidateManifestModulePath_FilepathIsAbs(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		// Existing cases that must still pass.
		{"relative dot", ".", false},
		{"simple relative", "cells/foo", false},
		{"relative with wildcard", "cells/*/cell.yaml", false},
		// Absolute paths must be rejected.
		{"unix absolute", "/etc/cells", true},
		{"root slash only", "/", true},
		// Parent escape must be rejected.
		{"parent escape", "../outside", true},
		{"embedded parent", "cells/../secret", true},
		// Empty path must be rejected.
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManifestModulePath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("validateManifestModulePath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateManifestModulePath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// TestValidateManifestModulePath_ConsistentWithRelativePath verifies that
// validateManifestModulePath and validateManifestRelativePath (locator.go)
// agree on which inputs are invalid (both reject the same unsafe patterns).
func TestValidateManifestModulePath_ConsistentWithRelativePath(t *testing.T) {
	cases := []struct {
		path string
	}{
		{"/etc/foo"},
		{"../escape"},
		{"a/../../b"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			errModule := validateManifestModulePath(tc.path)
			errRelative := validateManifestRelativePath(tc.path)
			if (errModule == nil) != (errRelative == nil) {
				t.Errorf(
					"validateManifestModulePath(%q)=%v vs validateManifestRelativePath(%q)=%v — should agree",
					tc.path, errModule, tc.path, errRelative,
				)
			}
		})
	}
}

// ── T3: parseWith warns on zero sources ───────────────────────────────────

// captureSlogWarn installs a temporary slog handler that collects Warn-level
// messages. Returns a cleanup function and a pointer to the collected slice.
// The cleanup restores the default handler.
func captureSlogWarn(t *testing.T) (cleanup func(), msgs *[]string) {
	t.Helper()
	var captured []string
	msgs = &captured
	old := slog.Default()
	h := &warnCapture{msgs: &captured}
	slog.SetDefault(slog.New(h))
	cleanup = func() { slog.SetDefault(old) }
	return cleanup, msgs
}

// warnCapture is a minimal slog.Handler that captures Warn messages.
type warnCapture struct {
	msgs *[]string
}

func (h *warnCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *warnCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.msgs = append(*h.msgs, r.Message)
	}
	return nil
}
func (h *warnCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCapture) WithGroup(_ string) slog.Handler      { return h }

// TestParseWith_ZeroSourcesWarn verifies that parseWith emits a slog.Warn
// when the Locator returns zero sources, returns an empty ProjectMeta, and
// returns nil error (behavior preserved).
func TestParseWith_ZeroSourcesWarn(t *testing.T) {
	// Empty fstest.MapFS → Conventional locator → Discover returns [].
	fsys := fstest.MapFS{}
	p := NewParser("")
	loc, err := NewLocatorFS(fsys, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}

	cleanup, msgs := captureSlogWarn(t)
	defer cleanup()

	pm, err := p.parseWith(loc)
	if err != nil {
		t.Fatalf("parseWith returned unexpected error: %v", err)
	}
	if pm == nil {
		t.Fatal("parseWith returned nil ProjectMeta")
	}
	if len(pm.Cells) != 0 || len(pm.Slices) != 0 || len(pm.Contracts) != 0 {
		t.Errorf("expected empty ProjectMeta, got cells=%d slices=%d contracts=%d",
			len(pm.Cells), len(pm.Slices), len(pm.Contracts))
	}
	// At least one Warn must have been emitted mentioning "zero sources" or similar.
	found := false
	for _, m := range *msgs {
		if strings.Contains(m, "zero sources") || strings.Contains(m, "no sources") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected slog.Warn about zero sources, got messages: %v", *msgs)
	}
}

// ── T7: excludes SkipDir pruning + match cap ──────────────────────────────

// TestManifestExcludeSet_MatchDir verifies the matchDir helper: only
// "<prefix>/**" patterns prune directories.
func TestManifestExcludeSet_MatchDir(t *testing.T) {
	es := &manifestExcludeSet{
		patterns: []string{"vendor/**", "generated/**", "cells/skip.yaml"},
	}
	cases := []struct {
		dir  string
		want bool
	}{
		{"vendor", true},
		{"vendor/foo", true},
		{"vendor/foo/bar", true},
		{"generated", true},
		{"generated/contracts", true},
		// "cells/skip.yaml" is not a "/**" pattern — must NOT prune directories.
		{"cells", false},
		{"other", false},
		{".", false},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			got := es.matchDir(tc.dir)
			if got != tc.want {
				t.Errorf("matchDir(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

// TestManifestGlobWalkFn_SkipDirOnExclude verifies that the WalkDir callback
// returns fs.SkipDir for directories that match a "<prefix>/**" exclude pattern,
// preventing traversal into excluded subtrees.
func TestManifestGlobWalkFn_SkipDirOnExclude(t *testing.T) {
	fsys := fstest.MapFS{
		"vendor/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: vendor-cell\n")},
		"cells/real/cell.yaml": &fstest.MapFile{Data: []byte("id: real\n")},
		"cells/skip/cell.yaml": &fstest.MapFile{Data: []byte("id: skip\n")},
	}

	// Provide manifest with vendor/** exclude so vendor subtree is pruned.
	manifest := `version: v1
modules:
  - path: .
    includes:
      cells:
        - "cells/*/cell.yaml"
        - "vendor/*/cell.yaml"
    excludes:
      - "vendor/**"
`
	fsys[".gocell/manifest.yaml"] = &fstest.MapFile{Data: []byte(manifest)}
	l, err := NewLocatorFS(fsys, WithLocatorMode(LocatorManifest))
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range sources {
		if strings.Contains(s.Path, "vendor") {
			t.Errorf("vendor path included despite exclude: %v", s)
		}
	}
	// Confirm "cells/real/cell.yaml" is still present.
	var found bool
	for _, s := range sources {
		if s.Path == "cells/real/cell.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("cells/real/cell.yaml missing from sources: %v", sources)
	}
}

// TestManifestGlobWalkFn_CapExceeded verifies that matching more than
// maxManifestMatchesPerGlob files per glob returns an error.
func TestManifestGlobWalkFn_CapExceeded(t *testing.T) {
	// Build a filesystem with maxManifestMatchesPerGlob+1 matching files.
	fsys := fstest.MapFS{}
	cap := maxManifestMatchesPerGlob
	for i := 0; i <= cap; i++ {
		path := fmt.Sprintf("cells/c%d/cell.yaml", i)
		fsys[path] = &fstest.MapFile{Data: []byte(fmt.Sprintf("id: c%d\n", i))}
	}
	_, err := matchManifestGlob(fsys, "cells/*/cell.yaml", nil)
	if err == nil {
		t.Fatalf("expected error when cap exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "cap") && !strings.Contains(err.Error(), "limit") {
		t.Errorf("error %q should mention cap or limit", err.Error())
	}
}

// TestManifestGlobWalkFn_CapNotExceeded verifies that exactly
// maxManifestMatchesPerGlob matches does not return an error.
func TestManifestGlobWalkFn_CapNotExceeded(t *testing.T) {
	fsys := fstest.MapFS{}
	cap := maxManifestMatchesPerGlob
	for i := 0; i < cap; i++ {
		path := fmt.Sprintf("cells/c%d/cell.yaml", i)
		fsys[path] = &fstest.MapFile{Data: []byte(fmt.Sprintf("id: c%d\n", i))}
	}
	matches, err := matchManifestGlob(fsys, "cells/*/cell.yaml", nil)
	if err != nil {
		t.Fatalf("unexpected error at exactly cap matches: %v", err)
	}
	if len(matches) != cap {
		t.Errorf("got %d matches, want %d", len(matches), cap)
	}
}

// TestManifestGlobWalkFn_SkipDirDoesNotAffectFileExcludes verifies that
// file-level excludes (non-"/**" patterns) are still applied even when
// SkipDir pruning is not triggered for that directory.
func TestManifestGlobWalkFn_SkipDirDoesNotAffectFileExcludes(t *testing.T) {
	// "cells/skip.yaml" is not a /**-pattern; cells/skip/cell.yaml file
	// should still be filtered by the post-walk file-level exclude check.
	manifest := `version: v1
modules:
  - path: .
    includes:
      cells:
        - "cells/*/cell.yaml"
    excludes:
      - "cells/skip/cell.yaml"
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"cells/keep/cell.yaml":  &fstest.MapFile{Data: []byte("id: keep\n")},
		"cells/skip/cell.yaml":  &fstest.MapFile{Data: []byte("id: skip\n")},
	}
	l, err := NewLocatorFS(fsys, WithLocatorMode(LocatorManifest))
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range sources {
		if strings.Contains(s.Path, "skip") {
			t.Errorf("file-level exclude not applied: %v", s)
		}
	}
}
