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
}
