package modrelease

import (
	"os"
	"path/filepath"
	"strings"
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
			name: "single-line exclude/retract keyword-anchored: untouched, require bumped",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v0.0.0\n" +
				"exclude github.com/ghbvf/gocell v0.0.0\n" +
				"retract v0.0.1\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v1.2.3\n" +
				"exclude github.com/ghbvf/gocell v0.0.0\n" +
				"retract v0.0.1\n",
			wantRequires: []string{"github.com/ghbvf/gocell/adapters/s3"},
		},
		{
			name: "block-form exclude/retract with internal path untouched, require bumped",
			in: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v0.0.0\n\n" +
				"exclude (\n" +
				"\tgithub.com/ghbvf/gocell v0.0.0\n" +
				"\tgithub.com/ghbvf/gocell/adapters/redis v0.0.0\n" +
				")\n\n" +
				"retract (\n\tv0.0.1\n)\n",
			want: "module gocell.example/x\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell/adapters/s3 v1.2.3\n\n" +
				"exclude (\n" +
				"\tgithub.com/ghbvf/gocell v0.0.0\n" +
				"\tgithub.com/ghbvf/gocell/adapters/redis v0.0.0\n" +
				")\n\n" +
				"retract (\n\tv0.0.1\n)\n",
			wantRequires: []string{"github.com/ghbvf/gocell/adapters/s3"},
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
		{"tests", true}, // tests root is a shared-helper library (depended on by adapters/postgres/pgtest)
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

// TestBumpTree exercises the actual release entry: it bumps every PUBLISHABLE
// member of a synthetic workspace (root + adapters), leaves a denied member
// (examples/*) untouched, preserves replace, and stamps Result.Dir.
func TestBumpTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/ghbvf/gocell\n\ngo 1.25\n")
	write("go.work", "go 1.25\n\nuse (\n\t.\n\t./adapters/a\n\t./adapters/b\n\t./examples/demo\n)\n")
	write("adapters/a/go.mod", "module github.com/ghbvf/gocell/adapters/a\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => ../../\n")
	write("adapters/b/go.mod", "module github.com/ghbvf/gocell/adapters/b\n\ngo 1.25\n\n"+
		"require (\n\tgithub.com/ghbvf/gocell v0.0.0\n\tgithub.com/ghbvf/gocell/adapters/a v0.0.0\n)\n")
	write("examples/demo/go.mod", "module github.com/ghbvf/gocell/examples/demo\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell v0.0.0\n")

	results, err := BumpTree(root, testVersion)
	if err != nil {
		t.Fatalf("BumpTree: %v", err)
	}
	// Publishable = root + adapters/a + adapters/b (examples/demo denied).
	if len(results) != 3 {
		t.Fatalf("want 3 publishable results, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.Dir == "" {
			t.Errorf("Result.Dir not stamped: %+v", r)
		}
	}
	a := mustRead(t, filepath.Join(root, "adapters", "a", "go.mod"))
	if !strings.Contains(a, "require github.com/ghbvf/gocell v1.2.3") {
		t.Errorf("adapters/a require not bumped:\n%s", a)
	}
	if !strings.Contains(a, "replace github.com/ghbvf/gocell => ../../") {
		t.Errorf("adapters/a replace must be preserved:\n%s", a)
	}
	b := mustRead(t, filepath.Join(root, "adapters", "b", "go.mod"))
	if strings.Contains(b, "v0.0.0") {
		t.Errorf("adapters/b still has v0.0.0 (both internal requires must bump):\n%s", b)
	}
	// Denied member is left as-is.
	demo := mustRead(t, filepath.Join(root, "examples", "demo", "go.mod"))
	if !strings.Contains(demo, "v0.0.0") {
		t.Errorf("examples/demo is denied and must be untouched, got:\n%s", demo)
	}

	// Invalid version rejected before touching the tree.
	if _, err := BumpTree(root, "1.2.3"); err == nil {
		t.Error("BumpTree must reject a non-canonical version")
	}
}

// TestTagPaths covers version validation + the root/satellite tag shapes against
// the real workspace.
func TestTagPaths(t *testing.T) {
	root := workspaceRootForTest(t)
	// Non-canonical and v2+ (Go semantic import versioning: no /vN module path)
	// must all be rejected.
	for _, bad := range []string{"1.2.3", "v1.2", "v1.2.3-rc1+meta", "", "v2.0.0", "v3.1.4"} {
		if _, err := TagPaths(root, bad); err == nil {
			t.Errorf("TagPaths must reject version %q", bad)
		}
	}
	tags, err := TagPaths(root, testVersion)
	if err != nil {
		t.Fatalf("TagPaths: %v", err)
	}
	if len(tags) < 2 {
		t.Fatalf("want >=2 tags, got %d", len(tags))
	}
	// Post-#1565 there is no root module (the core lives in the framework
	// submodule), so NO tag is bare — framework tags as "framework/<version>"
	// like every other satellite.
	if containsString(tags, testVersion) {
		t.Errorf("no module sits at the repo root post-#1565, so no bare %q tag should exist; got %v", testVersion, tags)
	}
	if !containsString(tags, "framework/"+testVersion) {
		t.Errorf("core module tag framework/%s missing from %v", testVersion, tags)
	}
	if !containsString(tags, "adapters/postgres/"+testVersion) {
		t.Errorf("satellite tag adapters/postgres/%s missing from %v", testVersion, tags)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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

// TestStripReplaceAndPin directly asserts StripResult.Requires (the output of
// collectPinnedRequires) for a table of fixture inputs. This is the missing
// coverage for the Requires field — TestInstallableStripTransform01 byte-freezes
// the transform output but does not exercise the returned metadata.
func TestStripReplaceAndPin(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantRequires []string // nil means empty (no internal requires pinned)
	}{
		{
			name: "standard: replace stripped, internal requires pinned and reported",
			in: "module github.com/ghbvf/gocell/cmd/gocell\n\ngo 1.25\n\n" +
				"require (\n" +
				"\tgithub.com/ghbvf/gocell v0.0.0\n" +
				"\tgithub.com/ghbvf/gocell/tools v0.0.0\n" +
				"\tgithub.com/google/uuid v1.6.0\n" +
				")\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n" +
				"replace github.com/ghbvf/gocell/tools => ../tools\n",
			wantRequires: []string{
				"github.com/ghbvf/gocell",
				"github.com/ghbvf/gocell/tools",
			},
		},
		{
			name: "single internal require, replace stripped",
			in: "module github.com/ghbvf/gocell/cmd/gocell\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell v0.0.0\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n",
			wantRequires: []string{"github.com/ghbvf/gocell"},
		},
		{
			name: "no internal requires — Requires is empty (anti-vacuity baseline)",
			in: "module github.com/ghbvf/gocell/cmd/gocell\n\ngo 1.25\n\n" +
				"require github.com/google/uuid v1.6.0\n",
			wantRequires: nil,
		},
		{
			name: "indirect internal require is pinned and reported",
			in: "module github.com/ghbvf/gocell/cmd/gocell\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocell v0.0.0 // indirect\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n",
			wantRequires: []string{"github.com/ghbvf/gocell"},
		},
		{
			name: "sibling-namespace false-match guard: gocellxyz must NOT appear in Requires",
			in: "module github.com/ghbvf/gocell/cmd/gocell\n\ngo 1.25\n\n" +
				"require github.com/ghbvf/gocellxyz/foo v0.4.0\n" +
				"require github.com/ghbvf/gocell v0.0.0\n\n" +
				"replace github.com/ghbvf/gocell => ../../\n",
			wantRequires: []string{"github.com/ghbvf/gocell"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			modPath := filepath.Clean(filepath.Join(dir, "go.mod"))
			if err := os.WriteFile(modPath, []byte(tt.in), 0o644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}
			res, err := StripReplaceAndPin(dir, testPrefix, testVersion)
			if err != nil {
				t.Fatalf("StripReplaceAndPin: %v", err)
			}
			// Assert StripResult.Requires matches the expected pinned internal paths.
			if !equalStrings(res.Requires, tt.wantRequires) {
				t.Errorf("StripResult.Requires = %v, want %v", res.Requires, tt.wantRequires)
			}
			// Sanity: the written go.mod must not contain replace.
			got, err := os.ReadFile(modPath)
			if err != nil {
				t.Fatalf("read go.mod: %v", err)
			}
			if strings.Contains(string(got), "replace") {
				t.Errorf("stripped go.mod still contains 'replace':\n%s", got)
			}
		})
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
