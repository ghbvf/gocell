package modrelease

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testPrefix  = "github.com/ghbvf/gocell"
	testVersion = "v1.2.3"
)

// TestBumpModule is the byte-level golden for the require-version rewrite. Each
// `want` MUST preserve replace directives, `// indirect` markers, external
// requires, the module line, and all surrounding bytes — only internal require
// versions change. This is the Hard load-bearing guard on "do NOT strip replace"
// (OTel-canonical published shape): a future edit that drops a replace line, or
// rewrites an external require, fails byte-diff here.
func TestBumpModule(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		want         string
		wantRequires []string
	}{
		{
			name: "block v0.0.0 + pseudo + indirect, replace+external+module untouched",
			in: "module github.com/ghbvf/gocell/adapters/postgres\n\ngo 1.25.11\n\n" +
				"require (\n" +
				"\tgithub.com/ghbvf/gocell v0.0.0\n" +
				"\tgithub.com/ghbvf/gocell/adapters/adapterutil v0.0.0\n" +
				"\tgithub.com/google/uuid v1.6.0\n" +
				")\n\n" +
				"require github.com/ghbvf/gocell/corecells v0.0.0-00010101000000-000000000000 // indirect\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n\n" +
				"replace github.com/ghbvf/gocell/adapters/adapterutil => ../adapterutil\n",
			want: "module github.com/ghbvf/gocell/adapters/postgres\n\ngo 1.25.11\n\n" +
				"require (\n" +
				"\tgithub.com/ghbvf/gocell v1.2.3\n" +
				"\tgithub.com/ghbvf/gocell/adapters/adapterutil v1.2.3\n" +
				"\tgithub.com/google/uuid v1.6.0\n" +
				")\n\n" +
				"require github.com/ghbvf/gocell/corecells v1.2.3 // indirect\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n\n" +
				"replace github.com/ghbvf/gocell/adapters/adapterutil => ../adapterutil\n",
			wantRequires: []string{
				"github.com/ghbvf/gocell",
				"github.com/ghbvf/gocell/adapters/adapterutil",
				"github.com/ghbvf/gocell/corecells",
			},
		},
		{
			name: "single-line require form, with and without indirect",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/tools v0.0.0\n" +
				"require github.com/ghbvf/gocell v0.0.0 // indirect\n\n" +
				"replace github.com/ghbvf/gocell/tools => ./tools\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/tools v1.2.3\n" +
				"require github.com/ghbvf/gocell v1.2.3 // indirect\n\n" +
				"replace github.com/ghbvf/gocell/tools => ./tools\n",
			wantRequires: []string{
				"github.com/ghbvf/gocell/tools",
				"github.com/ghbvf/gocell",
			},
		},
		{
			name: "steady real->real bump (not just v0.0.0)",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/redis v1.1.0\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/redis v1.2.3\n",
			wantRequires: []string{"github.com/ghbvf/gocell/adapters/redis"},
		},
		{
			name: "idempotent: already at target -> no change, no requires",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/redis v1.2.3\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/redis v1.2.3\n",
			wantRequires: nil,
		},
		{
			name: "no internal requires (root-like): unchanged",
			in: "module github.com/ghbvf/gocell\n\ngo 1.25.11\n\n" +
				"require (\n\tgithub.com/google/uuid v1.6.0\n\tgopkg.in/yaml.v3 v3.0.1\n)\n",
			want: "module github.com/ghbvf/gocell\n\ngo 1.25.11\n\n" +
				"require (\n\tgithub.com/google/uuid v1.6.0\n\tgopkg.in/yaml.v3 v3.0.1\n)\n",
			wantRequires: nil,
		},
		{
			name: "sibling-namespace false-match guard (gocellxyz must NOT match)",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocellxyz/foo v0.4.0\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v0.0.0\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocellxyz/foo v0.4.0\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v1.2.3\n",
			wantRequires: []string{"github.com/ghbvf/gocell/adapters/s3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			modPath := filepath.Clean(filepath.Join(dir, "go.mod"))
			if err := os.WriteFile(modPath, []byte(tt.in), 0o644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}
			res, err := BumpModule(dir, testPrefix, testVersion)
			if err != nil {
				t.Fatalf("BumpModule: %v", err)
			}
			got, err := os.ReadFile(modPath)
			if err != nil {
				t.Fatalf("read go.mod: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("go.mod bytes mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
			if !equalStrings(res.Requires, tt.wantRequires) {
				t.Errorf("Result.Requires = %v, want %v", res.Requires, tt.wantRequires)
			}
		})
	}
}

func TestIsPublishable(t *testing.T) {
	tests := []struct {
		dir  string
		want bool
	}{
		{".", true},
		{"adapters/postgres", true},
		{"adapters/adapterutil", true},
		{"corecells", true},
		{"cellmodules", true},
		{"tools", true},
		{"examples/demo", false},
		{"examples/ssobff", false},
		{"tests/integration", false},
		{"tests/testutil/pgshare", false},
		{"cmd/gocell", false},
		{"cmd/corebundle", false},
	}
	for _, tt := range tests {
		if got := IsPublishable(tt.dir); got != tt.want {
			t.Errorf("IsPublishable(%q) = %v, want %v", tt.dir, got, tt.want)
		}
	}
}

func TestTagPathsShape(t *testing.T) {
	// Root maps to a bare version tag; satellites to <dir>/<version>.
	mods := []struct {
		dir  string
		want string
	}{
		{".", "v1.2.3"},
		{"adapters/postgres", "adapters/postgres/v1.2.3"},
		{"tools", "tools/v1.2.3"},
	}
	for _, m := range mods {
		got := tagPathFor(m.dir, testVersion)
		if got != m.want {
			t.Errorf("tagPathFor(%q) = %q, want %q", m.dir, got, m.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
