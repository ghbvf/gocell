package workspace_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/workspace"
)

// writeTree writes files (relative path → content) under a fresh temp dir and
// returns the dir. Parent directories are created as needed.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	return root
}

func TestModules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string
		want    []workspace.Module
		wantErr string // substring; "" means no error
	}{
		{
			name: "single module use dot",
			files: map[string]string{
				"go.work":                  "go 1.25\n\nuse .\n",
				"go.mod":                   "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				".gocell/manifest.yaml":    "version: v1\nmodules:\n  - path: .\n",
				"cells/platform/cell.yaml": "id: platform\n",
			},
			want: []workspace.Module{
				{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
			},
		},
		{
			name: "multi module preserves go.work order with manifest subset",
			files: map[string]string{
				"go.work":                    "go 1.25\n\nuse (\n\t.\n\t./mdm\n)\n",
				"go.mod":                     "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				"mdm/go.mod":                 "module github.com/ghbvf/gocell/mdm\n\ngo 1.25\n",
				".gocell/manifest.yaml":      "version: v1\nmodules:\n  - path: .\n  - path: mdm\n",
				"cells/platform/cell.yaml":   "id: platform\n",
				"mdm/cells/winmdm/cell.yaml": "id: winmdm\n",
			},
			want: []workspace.Module{
				{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
				{Dir: "mdm", ImportPath: "github.com/ghbvf/gocell/mdm"},
			},
		},
		{
			name: "go.work superset of manifest is allowed (pure-tool module)",
			files: map[string]string{
				"go.work":                  "go 1.25\n\nuse (\n\t.\n\t./tools\n)\n",
				"go.mod":                   "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				"tools/go.mod":             "module github.com/ghbvf/gocell/tools\n\ngo 1.25\n",
				".gocell/manifest.yaml":    "version: v1\nmodules:\n  - path: .\n",
				"cells/platform/cell.yaml": "id: platform\n",
			},
			want: []workspace.Module{
				{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
				{Dir: "tools", ImportPath: "github.com/ghbvf/gocell/tools"},
			},
		},
		{
			name: "no manifest skips cross-check (fixture workspace)",
			files: map[string]string{
				"go.work":          "go 1.25\n\nuse (\n\t./core\n\t./satellite\n)\n",
				"core/go.mod":      "module example.test/core\n\ngo 1.25\n",
				"satellite/go.mod": "module example.test/satellite\n\ngo 1.25\n",
			},
			want: []workspace.Module{
				{Dir: "core", ImportPath: "example.test/core"},
				{Dir: "satellite", ImportPath: "example.test/satellite"},
			},
		},
		{
			name: "undeclared go.work member carrying metadata fails closed (reverse closure)",
			files: map[string]string{
				"go.work":                       "go 1.25\n\nuse (\n\t.\n\t./satellite\n)\n",
				"go.mod":                        "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				"satellite/go.mod":              "module github.com/ghbvf/gocell/satellite\n\ngo 1.25\n",
				".gocell/manifest.yaml":         "version: v1\nmodules:\n  - path: .\n",
				"cells/platform/cell.yaml":      "id: platform\n",
				"satellite/cells/sat/cell.yaml": "id: sat\n",
			},
			// satellite is a go.work member, absent from manifest, yet carries a
			// cell.yaml — metadata tooling would never discover it. Fail closed.
			wantErr: "carry GoCell metadata but are not declared",
		},
		{
			name: "manifest drift fails closed",
			files: map[string]string{
				"go.work":                    "go 1.25\n\nuse .\n",
				"go.mod":                     "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				".gocell/manifest.yaml":      "version: v1\nmodules:\n  - path: .\n  - path: mdm\n",
				"cells/platform/cell.yaml":   "id: platform\n",
				"mdm/cells/winmdm/cell.yaml": "id: winmdm\n",
			},
			wantErr: "not declared in go.work",
		},
		{
			name: "use dir missing go.mod fails closed",
			files: map[string]string{
				"go.work": "go 1.25\n\nuse (\n\t.\n\t./mdm\n)\n",
				"go.mod":  "module github.com/ghbvf/gocell\n\ngo 1.25\n",
				// mdm/ has no go.mod
				"mdm/keep.txt": "x\n",
			},
			wantErr: "read go.mod",
		},
		{
			name: "no go.work falls back to single module (external repo)",
			files: map[string]string{
				"go.mod": "module github.com/ghbvf/gocell\n\ngo 1.25\n",
			},
			want: []workspace.Module{
				{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
			},
		},
		{
			name: "neither go.work nor go.mod fails closed",
			files: map[string]string{
				"keep.txt": "x\n",
			},
			wantErr: "single-module root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := writeTree(t, tt.files)
			got, err := workspace.Modules(root)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Modules = %v, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Modules error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Modules unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Modules = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWorkspaceRoot verifies the go.work walk-up and the fail-loud absence
// behavior. Not parallel: it uses t.Chdir.
func TestWorkspaceRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.work":           "go 1.25\n\nuse .\n",
		"go.mod":            "module github.com/ghbvf/gocell\n\ngo 1.25\n",
		"cells/sub/keep.go": "package sub\n",
	})

	t.Run("found from nested subdir", func(t *testing.T) {
		t.Chdir(filepath.Join(root, "cells", "sub"))
		got, err := workspace.WorkspaceRoot()
		if err != nil {
			t.Fatalf("WorkspaceRoot unexpected error: %v", err)
		}
		// macOS /var symlink: compare resolved paths.
		gotResolved, _ := filepath.EvalSymlinks(got)
		wantResolved, _ := filepath.EvalSymlinks(root)
		if gotResolved != wantResolved {
			t.Fatalf("WorkspaceRoot = %q, want %q", gotResolved, wantResolved)
		}
	})

	t.Run("falls back to nearest go.mod when no go.work (external repo)", func(t *testing.T) {
		ext := writeTree(t, map[string]string{
			"go.mod":   "module example.test/ext\n\ngo 1.25\n",
			"sub/x.go": "package sub\n",
		})
		t.Chdir(filepath.Join(ext, "sub"))
		got, err := workspace.WorkspaceRoot()
		if err != nil {
			t.Fatalf("WorkspaceRoot (external single-module) unexpected error: %v", err)
		}
		gotResolved, _ := filepath.EvalSymlinks(got)
		wantResolved, _ := filepath.EvalSymlinks(ext)
		if gotResolved != wantResolved {
			t.Fatalf("WorkspaceRoot = %q, want the single-module root %q", gotResolved, wantResolved)
		}
	})

	t.Run("fail-loud when neither go.work nor go.mod above cwd", func(t *testing.T) {
		bare := t.TempDir() // no go.work and no go.mod in it or above within the test sandbox
		t.Chdir(bare)
		if _, err := workspace.WorkspaceRoot(); err == nil {
			t.Fatalf("WorkspaceRoot = nil error, want fail-loud (no go.work and no go.mod)")
		}
	})

	// go.work-first wins over a nearer go.mod — this is the exact #1555 bug scenario.
	// A nested sub/go.mod exists, but the root go.work must win so archtest anchors
	// to the WORKSPACE root, not a nested module's root.
	t.Run("go.work-first wins over a nearer go.mod", func(t *testing.T) {
		tree := writeTree(t, map[string]string{
			"go.work":    "go 1.25\n\nuse .\n",
			"go.mod":     "module github.com/ghbvf/gocell\n\ngo 1.25\n",
			"sub/go.mod": "module example.test/sub\n\ngo 1.25\n",
			"sub/pkg.go": "package sub\n",
		})
		// Chdir INTO sub — where the nearer go.mod lives.
		t.Chdir(filepath.Join(tree, "sub"))
		got, err := workspace.WorkspaceRoot()
		if err != nil {
			t.Fatalf("WorkspaceRoot (go.work-first) unexpected error: %v", err)
		}
		// Should resolve to tree (the go.work root), NOT tree/sub.
		gotResolved, _ := filepath.EvalSymlinks(got)
		wantResolved, _ := filepath.EvalSymlinks(tree)
		if gotResolved != wantResolved {
			t.Fatalf("WorkspaceRoot = %q, want workspace root %q (go.work must win over nearer go.mod)", gotResolved, wantResolved)
		}
	})
}

// TestModules_ManifestSymlinkEscape is the #1596 review F1 regression: a
// .gocell/manifest.yaml that is a symlink escaping the workspace root must fail
// closed. Pre-fix crossCheckManifest read it via os.DirFS (follows symlinks),
// silently sourcing the manifest contract from outside root; the fix routes the
// read through metadata.ReadManifestModulePathsRoot (os.Root), which rejects it.
func TestModules_ManifestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir() // OUTSIDE root
	// External manifest declares only ".", so a pre-fix read would pass the
	// forward cross-check (manifest ⊆ go.work.use) and succeed silently — the
	// escape. Only os.Root confinement turns it into a fail-closed error.
	if err := os.WriteFile(filepath.Join(external, "manifest.yaml"),
		[]byte("version: v1\nmodules:\n  - path: .\n"), 0o600); err != nil {
		t.Fatalf("write external manifest: %v", err)
	}
	for rel, content := range map[string]string{
		"go.work":           "go 1.25\n\nuse .\n",
		"go.mod":            "module example.test/x\n\ngo 1.25\n",
		"cells/x/cell.yaml": "id: x\n",
	} {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".gocell"), 0o755); err != nil {
		t.Fatalf("mkdir .gocell: %v", err)
	}
	// root/.gocell/manifest.yaml -> external/manifest.yaml (escapes root).
	if err := os.Symlink(filepath.Join(external, "manifest.yaml"),
		filepath.Join(root, ".gocell", "manifest.yaml")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	_, err := workspace.Modules(root)
	if err == nil {
		t.Fatalf("Modules() = nil error; want fail-closed (manifest symlink escapes root)")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Modules() error = %q, want root-confinement signal %q", err.Error(), "escapes")
	}
}

func TestExpandParentPrefix(t *testing.T) {
	// A representative post-#1565 member set: cmd/ and adapters/ each span
	// multiple members; framework is a single top-level member.
	mods := []workspace.Module{
		{Dir: "framework", ImportPath: "github.com/ghbvf/gocell/framework"},
		{Dir: "cmd/gocell", ImportPath: "github.com/ghbvf/gocell/cmd/gocell"},
		{Dir: "cmd/corebundle", ImportPath: "github.com/ghbvf/gocell/cmd/corebundle"},
		{Dir: "adapters/postgres", ImportPath: "github.com/ghbvf/gocell/adapters/postgres"},
		{Dir: "adapters/redis", ImportPath: "github.com/ghbvf/gocell/adapters/redis"},
	}
	tests := []struct {
		name    string
		mods    []workspace.Module
		pattern string
		want    []string
		wantOK  bool
	}{
		{
			name:    "multi-member cmd prefix expands to each member",
			mods:    mods,
			pattern: "./cmd/...",
			want:    []string{"./cmd/corebundle/...", "./cmd/gocell/..."},
			wantOK:  true,
		},
		{
			name:    "multi-member adapters prefix expands and sorts",
			mods:    mods,
			pattern: "./adapters/...",
			want:    []string{"./adapters/postgres/...", "./adapters/redis/..."},
			wantOK:  true,
		},
		{
			// framework is a single member that OWNS the dir exactly — the
			// caller's normal single-member resolution must handle it, not us.
			name:    "single owning member is not expanded",
			mods:    mods,
			pattern: "./framework/...",
			wantOK:  false,
		},
		{
			// A subpath UNDER a single member (framework/kernel) is owned by that
			// member; no member lives strictly under "framework/kernel".
			name:    "subpath under a single member is not expanded",
			mods:    mods,
			pattern: "./framework/kernel/...",
			wantOK:  false,
		},
		{
			name:    "prefix with no member under it match-zeroes (not expanded)",
			mods:    mods,
			pattern: "./examples/...",
			wantOK:  false,
		},
		{
			// Single-module fixture: the sole "." member owns nothing under a
			// subdir, so "./cmd/..." stays verbatim (loads in the one module).
			name:    "single-module fixture leaves pattern verbatim",
			mods:    []workspace.Module{{Dir: ".", ImportPath: "github.com/acme/app"}},
			pattern: "./cmd/...",
			wantOK:  false,
		},
		{
			name:    "non-recursive pattern is not expanded",
			mods:    mods,
			pattern: "./cmd/gocell",
			wantOK:  false,
		},
		{
			name:    "bare-root recursive pattern is not expanded",
			mods:    mods,
			pattern: "./...",
			wantOK:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := workspace.ExpandParentPrefix(tc.mods, tc.pattern)
			if ok != tc.wantOK {
				t.Fatalf("ExpandParentPrefix(%q) ok = %v, want %v (got %v)", tc.pattern, ok, tc.wantOK, got)
			}
			if tc.wantOK && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExpandParentPrefix(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}
