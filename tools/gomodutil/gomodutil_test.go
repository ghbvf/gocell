package gomodutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/gomodutil"
)

func TestValidateModulePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid github path", input: "github.com/acme/svc", wantErr: false},
		{name: "valid versioned path", input: "example.com/m/v2", wantErr: false},
		{name: "single segment", input: "foo", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "dotdot", input: "../foo", wantErr: true},
		{name: "embedded dotdot element", input: "foo/../bar", wantErr: true},
		{name: "backslash", input: `foo\bar`, wantErr: true},
		{name: "newline", input: "foo\nbar", wantErr: true},
		{name: "tab", input: "foo\tbar", wantErr: true},
		{name: "space", input: "foo bar", wantErr: true},
		// Regression: the prior hand-rolled regex silently admitted these
		// malformed paths, corrupting generated import statements (#1083 F1).
		{name: "trailing slash", input: "github.com/acme/svc/", wantErr: true},
		{name: "double slash", input: "github.com//acme", wantErr: true},
		{name: "leading dash", input: "-leadingdash/x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := gomodutil.ValidateModulePath(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateModulePath(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateModulePath(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestReadModulePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		goMod   string // "" means do not write go.mod
		want    string
		wantErr bool
	}{
		{
			name:  "simple module line",
			goMod: "module github.com/ghbvf/gocell\n\ngo 1.25\n",
			want:  "github.com/ghbvf/gocell",
		},
		{
			name:  "external module path",
			goMod: "module github.com/acme/svc\n\ngo 1.25\n",
			want:  "github.com/acme/svc",
		},
		{
			name:  "module line with trailing inline comment",
			goMod: "module github.com/acme/commented // pinned\n\ngo 1.25\n",
			want:  "github.com/acme/commented",
		},
		{
			name:  "leading comment lines before module",
			goMod: "// Code generated header.\n// keep.\nmodule github.com/acme/leadcomment\n\ngo 1.25\n",
			want:  "github.com/acme/leadcomment",
		},
		{
			name:    "missing go.mod",
			goMod:   "",
			wantErr: true,
		},
		{
			name:    "no module directive",
			goMod:   "go 1.25\n",
			wantErr: true,
		},
		{
			name:    "invalid trailing slash module path",
			goMod:   "module github.com/acme/svc/\n\ngo 1.25\n",
			wantErr: true,
		},
		{
			name:    "invalid backslash module path",
			goMod:   "module github.com\\acme\n\ngo 1.25\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.goMod != "" {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(tt.goMod), 0o600); err != nil {
					t.Fatalf("write go.mod: %v", err)
				}
			}

			got, err := gomodutil.ReadModulePath(root)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadModulePath(%q) = %q, want error", root, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadModulePath(%q) unexpected error: %v", root, err)
			}
			if got != tt.want {
				t.Fatalf("ReadModulePath(%q) = %q, want %q", root, got, tt.want)
			}
		})
	}
}

func TestReadWorkUseDirs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		goWork  string // "" means do not write go.work
		want    []string
		wantErr bool
	}{
		{
			name:   "single use dot",
			goWork: "go 1.25\n\nuse .\n",
			want:   []string{"."},
		},
		{
			name:   "block form multiple modules",
			goWork: "go 1.25\n\nuse (\n\t.\n\t./mdm\n\t./examples/ssobff\n)\n",
			want:   []string{".", "mdm", "examples/ssobff"},
		},
		{
			name:   "trailing-slash and dot-prefix cleaned",
			goWork: "go 1.25\n\nuse (\n\t./mdm/\n\t./zerotrust\n)\n",
			want:   []string{"mdm", "zerotrust"},
		},
		{
			name:    "missing go.work",
			goWork:  "",
			wantErr: true,
		},
		{
			name:    "malformed go.work",
			goWork:  "this is not a go.work file {{{\n",
			wantErr: true,
		},
		{
			name:    "dotdot escape rejected",
			goWork:  "go 1.25\n\nuse ../escape\n",
			wantErr: true,
		},
		{
			name:    "absolute path rejected",
			goWork:  "go 1.25\n\nuse /abs/path\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.goWork != "" {
				if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(tt.goWork), 0o600); err != nil {
					t.Fatalf("write go.work: %v", err)
				}
			}

			got, err := gomodutil.ReadWorkUseDirs(root)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadWorkUseDirs(%q) = %v, want error", root, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadWorkUseDirs(%q) unexpected error: %v", root, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ReadWorkUseDirs(%q) = %v, want %v", root, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ReadWorkUseDirs(%q)[%d] = %q, want %q", root, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestReadWorkUseDirs_SymlinkEscape covers the symlink path-traversal guard:
// a `use` dir that is a symlink resolving OUTSIDE the workspace root must be
// rejected (else `go list ./<dir>/...` would scan packages outside the repo),
// while a symlink resolving WITHIN the root is accepted. Not table-driven
// because it needs real symlink setup. Not parallel (filesystem-heavy, but
// isolated temp dirs so it is safe to parallelize; kept serial for clarity).
func TestReadWorkUseDirs_SymlinkEscape(t *testing.T) {
	t.Run("symlink escaping the workspace root is rejected", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir() // a sibling dir OUTSIDE root
		if err := os.WriteFile(filepath.Join(external, "go.mod"),
			[]byte("module example.test/outside\n\ngo 1.25\n"), 0o600); err != nil {
			t.Fatalf("write external go.mod: %v", err)
		}
		if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlink unsupported on this platform: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.work"),
			[]byte("go 1.25\n\nuse ./linked\n"), 0o600); err != nil {
			t.Fatalf("write go.work: %v", err)
		}

		got, err := gomodutil.ReadWorkUseDirs(root)
		if err == nil {
			t.Fatalf("ReadWorkUseDirs = %v, want error (symlink escapes workspace root)", got)
		}
		if !strings.Contains(err.Error(), "escapes the workspace root") {
			t.Fatalf("error = %q, want substring %q", err.Error(), "escapes the workspace root")
		}
	})

	t.Run("symlink resolving within the workspace root is accepted", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
			t.Fatalf("mkdir real: %v", err)
		}
		if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlink unsupported on this platform: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.work"),
			[]byte("go 1.25\n\nuse ./linked\n"), 0o600); err != nil {
			t.Fatalf("write go.work: %v", err)
		}

		got, err := gomodutil.ReadWorkUseDirs(root)
		if err != nil {
			t.Fatalf("ReadWorkUseDirs unexpected error for in-root symlink: %v", err)
		}
		if len(got) != 1 || got[0] != "linked" {
			t.Fatalf("ReadWorkUseDirs = %v, want [linked]", got)
		}
	})
}

func TestReadReplaceExclude(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		goMod        string // "" means do not write go.mod
		wantReplaces int
		wantExcludes int
		wantErr      bool
	}{
		{
			name:  "clean module has neither",
			goMod: "module github.com/acme/svc\n\ngo 1.25\n\nrequire github.com/x/y v1.2.3\n",
		},
		{
			name:         "single local replace",
			goMod:        "module github.com/acme/svc\n\ngo 1.25\n\nrequire github.com/x/y v1.2.3\n\nreplace github.com/x/y => ../../y\n",
			wantReplaces: 1,
		},
		{
			name:         "versioned module replace",
			goMod:        "module m\n\ngo 1.25\n\nreplace github.com/x/y v1.0.0 => github.com/x/y v1.0.1\n",
			wantReplaces: 1,
		},
		{
			name:         "exclude only",
			goMod:        "module m\n\ngo 1.25\n\nexclude github.com/x/y v1.2.3\n",
			wantExcludes: 1,
		},
		{
			name: "block replace and block exclude",
			goMod: "module m\n\ngo 1.25\n\n" +
				"replace (\n\tgithub.com/a/b => ../b\n\tgithub.com/c/d v1.0.0 => github.com/c/d v1.0.1\n)\n\n" +
				"exclude (\n\tgithub.com/x/y v1.0.0\n\tgithub.com/x/y v1.1.0\n)\n",
			wantReplaces: 2,
			wantExcludes: 2,
		},
		{
			name:    "missing go.mod",
			goMod:   "",
			wantErr: true,
		},
		{
			name:    "malformed go.mod (unterminated block)",
			goMod:   "module m\n\ngo 1.25\n\nrequire (\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.goMod != "" {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(tt.goMod), 0o600); err != nil {
					t.Fatalf("write go.mod: %v", err)
				}
			}

			replaces, excludes, err := gomodutil.ReadReplaceExclude(root)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadReplaceExclude(%q) = (%v, %v), want error", root, replaces, excludes)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadReplaceExclude(%q) unexpected error: %v", root, err)
			}
			if len(replaces) != tt.wantReplaces {
				t.Errorf("ReadReplaceExclude(%q) replaces = %d (%v), want %d", root, len(replaces), replaces, tt.wantReplaces)
			}
			if len(excludes) != tt.wantExcludes {
				t.Errorf("ReadReplaceExclude(%q) excludes = %d (%v), want %d", root, len(excludes), excludes, tt.wantExcludes)
			}
		})
	}
}
