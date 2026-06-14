package packagesload

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/workspace"
)

// representativeMods is a post-#1565 member set: cmd/ and adapters/ each span
// multiple members; framework is a single top-level member.
var representativeMods = []workspace.Module{
	{Dir: "framework", ImportPath: "github.com/ghbvf/gocell/framework"},
	{Dir: "cmd/gocell", ImportPath: "github.com/ghbvf/gocell/cmd/gocell"},
	{Dir: "cmd/corebundle", ImportPath: "github.com/ghbvf/gocell/cmd/corebundle"},
	{Dir: "adapters/postgres", ImportPath: "github.com/ghbvf/gocell/adapters/postgres"},
	{Dir: "adapters/redis", ImportPath: "github.com/ghbvf/gocell/adapters/redis"},
}

func TestExpandParentPrefix(t *testing.T) {
	tests := []struct {
		name    string
		mods    []workspace.Module
		pattern string
		want    []string
		wantOK  bool
	}{
		{
			name:    "multi-member cmd prefix expands to each member",
			mods:    representativeMods,
			pattern: "./cmd/...",
			want:    []string{"./cmd/corebundle/...", "./cmd/gocell/..."},
			wantOK:  true,
		},
		{
			name:    "multi-member adapters prefix expands and sorts",
			mods:    representativeMods,
			pattern: "./adapters/...",
			want:    []string{"./adapters/postgres/...", "./adapters/redis/..."},
			wantOK:  true,
		},
		{
			// framework is a single member that OWNS the dir exactly — the
			// caller's normal single-member resolution must handle it, not us.
			name:    "single owning member is not expanded",
			mods:    representativeMods,
			pattern: "./framework/...",
			wantOK:  false,
		},
		{
			name:    "subpath under a single member is not expanded",
			mods:    representativeMods,
			pattern: "./framework/kernel/...",
			wantOK:  false,
		},
		{
			name:    "prefix with no member under it match-zeroes (not expanded)",
			mods:    representativeMods,
			pattern: "./examples/...",
			wantOK:  false,
		},
		{
			name:    "single-module fixture leaves pattern verbatim",
			mods:    []workspace.Module{{Dir: ".", ImportPath: "github.com/acme/app"}},
			pattern: "./cmd/...",
			wantOK:  false,
		},
		{
			name:    "non-recursive pattern is not expanded",
			mods:    representativeMods,
			pattern: "./cmd/gocell",
			wantOK:  false,
		},
		{
			name:    "bare-root recursive pattern is not expanded",
			mods:    representativeMods,
			pattern: "./...",
			wantOK:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := expandParentPrefix(tc.mods, tc.pattern)
			if ok != tc.wantOK {
				t.Fatalf("expandParentPrefix(%q) ok = %v, want %v (got %v)", tc.pattern, ok, tc.wantOK, got)
			}
			if tc.wantOK && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("expandParentPrefix(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestSplitWorkspacePattern(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		wantDir     string
		wantPattern string
	}{
		{"bare root maps to framework member", "./...", frameworkModuleDir, "./..."},
		{"member subpath", "./cmd/gocell/...", "cmd/gocell", "./..."},
		{"member by import path", "github.com/ghbvf/gocell/adapters/redis/x", "adapters/redis", "github.com/ghbvf/gocell/adapters/redis/x"},
		{"unowned parent prefix skips", "./examples/...", skipPatternDir, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, pattern := splitWorkspacePattern(representativeMods, tc.pattern)
			if dir != tc.wantDir || pattern != tc.wantPattern {
				t.Fatalf("splitWorkspacePattern(%q) = (%q, %q), want (%q, %q)",
					tc.pattern, dir, pattern, tc.wantDir, tc.wantPattern)
			}
		})
	}
}

func TestSplitWorkspacePattern_RoutesKnownSatellite(t *testing.T) {
	mods := []workspace.Module{
		{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
		{Dir: "tools", ImportPath: "github.com/ghbvf/gocell/tools"},
	}

	dir, pattern := splitWorkspacePattern(mods, "./tools/archtest/internal/typeseval/...")
	if dir != "tools" || pattern != "./archtest/internal/typeseval/..." {
		t.Fatalf("split = (%q, %q), want (tools, ./archtest/internal/typeseval/...)", dir, pattern)
	}

	// No framework member here, so a root-relative non-member prefix falls back to the
	// root (".") form rather than the skip sentinel.
	dir, pattern = splitWorkspacePattern(mods, "./framework/pkg/...")
	if dir != "." || pattern != "./framework/pkg/..." {
		t.Fatalf("split = (%q, %q), want (., ./framework/pkg/...)", dir, pattern)
	}

	dir, pattern = splitWorkspacePattern(mods, "github.com/ghbvf/gocell/tools/archtest/internal/scanner")
	if dir != "tools" || pattern != "github.com/ghbvf/gocell/tools/archtest/internal/scanner" {
		t.Fatalf("split = (%q, %q), want (tools, <importpath>)", dir, pattern)
	}
}

func TestSplitWorkspacePattern_PrefersLongestSatellitePrefix(t *testing.T) {
	mods := []workspace.Module{
		{Dir: ".", ImportPath: "github.com/ghbvf/gocell"},
		{Dir: "tools", ImportPath: "github.com/ghbvf/gocell/tools"},
		{Dir: "tools/nested", ImportPath: "github.com/ghbvf/gocell/tools/nested"},
	}

	dir, pattern := splitWorkspacePattern(mods, "./tools/nested/pkg/...")
	if dir != "tools/nested" || pattern != "./pkg/..." {
		t.Fatalf("split = (%q, %q), want (tools/nested, ./pkg/...)", dir, pattern)
	}

	dir, pattern = splitWorkspacePattern(mods, "github.com/ghbvf/gocell/tools/nested/pkg")
	if dir != "tools/nested" || pattern != "github.com/ghbvf/gocell/tools/nested/pkg" {
		t.Fatalf("split = (%q, %q), want (tools/nested, <importpath>)", dir, pattern)
	}
}

// TestLoadWorkspace_ExpandsSatelliteMember exercises the full expand → group → load
// path against a real (dependency-free) go.work workspace: "./examples/..." is owned
// by no single member, so LoadWorkspace must expand it to examples/foo and load that
// member — the satellite coverage the pre-#2147 loader silently dropped.
func TestLoadWorkspace_ExpandsSatelliteMember(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, "go.work", "go 1.25\n\nuse ./examples/foo\n")
	writeWS(t, root, "examples/foo/go.mod", "module example.com/foo\n\ngo 1.25\n")
	writeWS(t, root, "examples/foo/foo.go", "package foo\n\n// F is a satellite-member symbol.\nfunc F() int { return 1 }\n")

	cfg := packages.Config{Context: context.Background(), Mode: packages.NeedName | packages.NeedFiles}
	pkgs, loadErrs, err := LoadWorkspace(root, cfg, "./examples/...")
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	if len(loadErrs) > 0 {
		t.Fatalf("LoadWorkspace load errors: %v", loadErrs)
	}
	if !containsPkgPath(pkgs, "example.com/foo") {
		t.Fatalf("LoadWorkspace did not load the satellite member example.com/foo; got %v", pkgPaths(pkgs))
	}
}

// TestLoadWorkspace_SingleModuleFallback verifies a tree with no go.work loads as one
// ModeModule module at root (the no-workspace fallback path).
func TestLoadWorkspace_SingleModuleFallback(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, "go.mod", "module example.com/solo\n\ngo 1.25\n")
	writeWS(t, root, "pkg/util/util.go", "package util\n\nfunc V() int { return 2 }\n")

	cfg := packages.Config{Context: context.Background(), Mode: packages.NeedName | packages.NeedFiles}
	pkgs, loadErrs, err := LoadWorkspace(root, cfg, "./pkg/...")
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	if len(loadErrs) > 0 {
		t.Fatalf("LoadWorkspace load errors: %v", loadErrs)
	}
	if !containsPkgPath(pkgs, "example.com/solo/pkg/util") {
		t.Fatalf("single-module fallback did not load example.com/solo/pkg/util; got %v", pkgPaths(pkgs))
	}
}

func writeWS(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pkgPaths(pkgs []*packages.Package) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.PkgPath)
	}
	return out
}

func containsPkgPath(pkgs []*packages.Package, path string) bool {
	for _, p := range pkgs {
		if p.PkgPath == path {
			return true
		}
	}
	return false
}
