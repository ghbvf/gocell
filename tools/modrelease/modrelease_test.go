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

// TestBumpTree exercises the actual release entry (the PIN set): it rewrites the
// internal requires of EVERY workspace MEMBER of a synthetic workspace — the
// publishable libraries AND the non-publishable members that `go work sync`
// would otherwise rewrite (examples/*, cmd/*, tests/* subdirs) — preserving
// replace and stamping Result.Dir. The pin set is a SUPERSET of the publishable
// TAG set ([PublishableModules]/[TagPaths]).
//
// #2212: pinning ONLY the publishable set left tests/*, cmd/*, examples/*
// internal requires at v0.0.0, so the release `Verify pinned tree` step's
// `go work sync` rewrote them to the release version and `git diff --exit-code`
// found uncommitted drift, failing the first stable release since #1565. The pin
// set must cover every member with an internal require so `go work sync` is a
// no-op after the pin commit.
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
	// go.work includes the non-publishable members go work sync rewrites:
	// examples/demo, cmd/foo (a binary), tests/integration (a tests/ subdir).
	write("go.work", "go 1.25\n\nuse (\n\t.\n\t./adapters/a\n\t./adapters/b\n"+
		"\t./examples/demo\n\t./cmd/foo\n\t./tests/integration\n)\n")
	write("adapters/a/go.mod", "module github.com/ghbvf/gocell/adapters/a\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => ../../\n")
	write("adapters/b/go.mod", "module github.com/ghbvf/gocell/adapters/b\n\ngo 1.25\n\n"+
		"require (\n\tgithub.com/ghbvf/gocell v0.0.0\n\tgithub.com/ghbvf/gocell/adapters/a v0.0.0\n)\n")
	write("examples/demo/go.mod", "module github.com/ghbvf/gocell/examples/demo\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => ../../\n")
	write("cmd/foo/go.mod", "module github.com/ghbvf/gocell/cmd/foo\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => ../../\n")
	write("tests/integration/go.mod", "module github.com/ghbvf/gocell/tests/integration\n\ngo 1.25\n\n"+
		"require github.com/ghbvf/gocell/adapters/a v0.0.0\n\n"+
		"replace github.com/ghbvf/gocell/adapters/a => ../../adapters/a\n")

	results, err := BumpTree(root, testVersion)
	if err != nil {
		t.Fatalf("BumpTree: %v", err)
	}
	for _, r := range results {
		if r.Dir == "" {
			t.Errorf("Result.Dir not stamped: %+v", r)
		}
	}

	// Every member with an internal require must be bumped — publishable AND
	// the non-publishable members go work sync would otherwise drift (#2212).
	// Each carries a replace that must be preserved.
	for _, m := range []struct{ dir, replace string }{
		{"adapters/a", "replace github.com/ghbvf/gocell => ../../"},
		{"examples/demo", "replace github.com/ghbvf/gocell => ../../"},
		{"cmd/foo", "replace github.com/ghbvf/gocell => ../../"},
		{"tests/integration", "replace github.com/ghbvf/gocell/adapters/a => ../../adapters/a"},
	} {
		got := mustRead(t, filepath.Join(root, filepath.FromSlash(m.dir), "go.mod"))
		if strings.Contains(got, "v0.0.0") {
			t.Errorf("%s internal require not pinned (still v0.0.0) — pin set must cover non-publishable members (#2212):\n%s", m.dir, got)
		}
		if !strings.Contains(got, m.replace) {
			t.Errorf("%s replace must be preserved:\n%s", m.dir, got)
		}
	}
	// adapters/b has two internal requires; both must bump.
	b := mustRead(t, filepath.Join(root, "adapters", "b", "go.mod"))
	if strings.Contains(b, "v0.0.0") {
		t.Errorf("adapters/b still has v0.0.0 (both internal requires must bump):\n%s", b)
	}

	// pin set ⊇ tag set: every publishable (tag-set) member appears in the
	// pin-set results, so the pin commit can never be a strict subset of the
	// tagged tree (the #2212 failure mode). Both BumpTree results and
	// PublishableModules derive Dir from the same workspace.Modules enumeration
	// (go.work use-dirs, filepath.Clean'd — no "./" prefix), so the map lookup
	// keys align and this comparison is not vacuous.
	pub, err := PublishableModules(root)
	if err != nil {
		t.Fatalf("PublishableModules: %v", err)
	}
	resultDirs := make(map[string]bool, len(results))
	for _, r := range results {
		resultDirs[r.Dir] = true
	}
	for _, m := range pub {
		if !resultDirs[m.Dir] {
			t.Errorf("publishable (tag-set) member %q missing from pin-set results — pin must be a superset of tag", m.Dir)
		}
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
