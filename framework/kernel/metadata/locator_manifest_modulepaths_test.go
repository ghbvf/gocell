package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// TestReadManifestModulePaths verifies the exported accessor returns the
// declared module disk paths in order and fails closed on a malformed manifest
// (it shares loadManifest's validation). It is the workspace enumerator's
// metadata-module source, cross-checked against go.work's `use` directives.
func TestReadManifestModulePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fsys    fstest.MapFS
		want    []string
		wantErr bool
	}{
		{
			name: "single module dot",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml":    &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n")},
				"cells/platform/cell.yaml": &fstest.MapFile{Data: []byte("id: platform\n")},
			},
			want: []string{"."},
		},
		{
			name: "multi module declaration order preserved",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml":      &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n  - path: mdm\n")},
				"cells/platform/cell.yaml":   &fstest.MapFile{Data: []byte("id: platform\n")},
				"mdm/cells/winmdm/cell.yaml": &fstest.MapFile{Data: []byte("id: winmdm\n")},
			},
			want: []string{".", "mdm"},
		},
		{
			name: "missing manifest fails closed",
			fsys: fstest.MapFS{
				"cells/platform/cell.yaml": &fstest.MapFile{Data: []byte("id: platform\n")},
			},
			wantErr: true,
		},
		{
			name: "bad version fails closed",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v2\nmodules:\n  - path: .\n")},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadManifestModulePaths(tt.fsys, DefaultManifestPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadManifestModulePaths = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadManifestModulePaths unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ReadManifestModulePaths = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReadManifestModulePathsRoot covers the os.Root-confined disk wrapper
// (the #1592 review-F1 single source for tools/workspace's manifest
// cross-check). It is exercised only cross-package in production (by
// tools/workspace), which CI's per-package coverage — run without
// -coverpkg=./... — does not credit back to this package; these in-package
// cases keep the wrapper covered on its own. Three behaviors: it returns the
// declared module paths for a normal on-disk layout, fails closed when the
// root cannot be opened, and rejects a .gocell/manifest.yaml that is a symlink
// escaping the root ("path escapes from parent" at the syscall layer).
func TestReadManifestModulePathsRoot(t *testing.T) {
	t.Run("normal on-disk layout returns module paths in declaration order", func(t *testing.T) {
		assertReadManifestModulePathsRootNormal(t)
	})

	t.Run("open-root error on missing root fails closed", func(t *testing.T) {
		assertReadManifestModulePathsRootMissing(t)
	})

	t.Run("symlinked manifest escaping root fails closed", func(t *testing.T) {
		assertReadManifestModulePathsRootSymlinkEscape(t)
	})
}

func assertReadManifestModulePathsRootNormal(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n  - path: sub\n")
	// modules[1].path "sub" must exist and be a directory (validateManifestModuleDir).
	writeFile(t, filepath.Join(root, "sub", "cells", "x", "cell.yaml"),
		cellYAML("x", "team", "x.primary"))

	got, err := ReadManifestModulePathsRoot(root, DefaultManifestPath)
	if err != nil {
		t.Fatalf("ReadManifestModulePathsRoot: %v", err)
	}
	if want := []string{".", "sub"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadManifestModulePathsRoot = %v, want %v", got, want)
	}
}

func assertReadManifestModulePathsRootMissing(t *testing.T) {
	t.Helper()

	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	_, err := ReadManifestModulePathsRoot(missing, DefaultManifestPath)
	if err == nil {
		t.Fatalf("ReadManifestModulePathsRoot(%q) = nil error; want open-root failure", missing)
	}
	if !strings.Contains(err.Error(), "open root") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "open root")
	}
}

func assertReadManifestModulePathsRootSymlinkEscape(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	external := t.TempDir() // OUTSIDE root
	writeFile(t, filepath.Join(external, "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n")
	if err := os.MkdirAll(filepath.Join(root, ".gocell"), 0o755); err != nil {
		t.Fatalf("MkdirAll .gocell: %v", err)
	}
	// root/.gocell/manifest.yaml -> external/manifest.yaml (escapes root).
	if err := os.Symlink(filepath.Join(external, "manifest.yaml"),
		filepath.Join(root, ".gocell", "manifest.yaml")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	_, err := ReadManifestModulePathsRoot(root, DefaultManifestPath)
	if err == nil {
		t.Fatalf("ReadManifestModulePathsRoot = nil error; want fail-closed (manifest symlink escapes root)")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %q, want root-confinement signal %q", err.Error(), "escapes")
	}
}
