package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// wsmm* mirror the module / package paths of the reverse fixture under
// tools/archtest/testdata/workspace-multimodule (a real two-module workspace
// whose satellite module path is a subpath of the core module path).
const (
	wsmmCoreModule = "example.test/wsmm"
	wsmmSatModule  = "example.test/wsmm/satellite"
	wsmmCorePkg    = wsmmCoreModule + "/cells/corecell"
	wsmmSatPkg     = wsmmSatModule + "/cells/satcell"
)

// useWorkspaceMultiModuleFixture returns the abs path of the multi-module
// fixture and points GOWORK at its go.work so the workspace loaders resolve the
// fixture's members (not the repo's go.work, which does not list them). Because
// it mutates process env via t.Setenv, callers must not run t.Parallel.
func useWorkspaceMultiModuleFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(repoRoot(t), "tools", "archtest", "testdata", "workspace-multimodule")
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		t.Fatalf("fixture go.work missing at %s: %v", root, err)
	}
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	return root
}

// TestRunGraphWorkspaceMultiModule is the F3 regression: `gocell graph` with the
// default pattern against a workspace must span EVERY go.work member via
// relative-dir patterns. The pre-fix code expanded the default to
// "<importPath>/..." module-path patterns, which makes `go` try to fetch the
// satellite as an external dependency ("unrecognized import path") instead of
// resolving the local member.
func TestRunGraphWorkspaceMultiModule(t *testing.T) {
	root := useWorkspaceMultiModuleFixture(t)

	var buf bytes.Buffer
	if err := executeGraph(graphOptions{
		Format:  graphFormatJSON,
		Pattern: defaultGraphPattern,
		Root:    root,
		Out:     &buf,
	}); err != nil {
		t.Fatalf("executeGraph(workspace-multimodule): %v", err)
	}

	var graph struct {
		Modules  []string `json:"modules"`
		Packages []struct {
			ID    string `json:"id"`
			Layer string `json:"layer"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(buf.Bytes(), &graph); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput=%s", err, buf.String())
	}

	mods := map[string]bool{}
	for _, m := range graph.Modules {
		mods[m] = true
	}
	if !mods[wsmmCoreModule] || !mods[wsmmSatModule] {
		t.Fatalf("Modules = %v, want both %q and %q", graph.Modules, wsmmCoreModule, wsmmSatModule)
	}

	pkgs := map[string]string{}
	for _, p := range graph.Packages {
		pkgs[p.ID] = p.Layer
	}
	if _, ok := pkgs[wsmmCorePkg]; !ok {
		t.Errorf("core package %q missing from graph", wsmmCorePkg)
	}
	if layer, ok := pkgs[wsmmSatPkg]; !ok {
		t.Errorf("satellite package %q missing from graph (nested module silently dropped — the F3 bug)", wsmmSatPkg)
	} else if layer != string(kerneldepgraph.LayerCells) {
		t.Errorf("satellite package %q layer = %q, want %q", wsmmSatPkg, layer, kerneldepgraph.LayerCells)
	}
}

// TestGenerateCatalogWorkspaceMultiModule is the F1 regression: `gocell generate
// catalog` must emit satellite packages. The pre-fix loader was a single-module
// depgraph.Load("./...") that dropped every non-core workspace member.
func TestGenerateCatalogWorkspaceMultiModule(t *testing.T) {
	root := useWorkspaceMultiModuleFixture(t)
	outPath := filepath.Join(t.TempDir(), "catalog_gen.go")

	if err := generateCatalog([]string{
		"--out=" + outPath,
		"--package=testpkg",
		"--root=" + root,
	}); err != nil {
		t.Fatalf("generateCatalog(workspace-multimodule): %v", err)
	}

	content, err := os.ReadFile(outPath) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	src := string(content)
	if !strings.Contains(src, wsmmCorePkg) {
		t.Errorf("generated catalog missing core package %q", wsmmCorePkg)
	}
	if !strings.Contains(src, wsmmSatPkg) {
		t.Errorf("generated catalog missing satellite package %q — satellite silently dropped (the F1 bug)", wsmmSatPkg)
	}
}

// TestAttachPackageDepsWorkspaceMultiModule is the F2 regression: `gocell export
// catalog --include=packageDeps` must include satellite packages in the exported
// graph. Exercises attachPackageDeps directly (the export packageDeps entry).
func TestAttachPackageDepsWorkspaceMultiModule(t *testing.T) {
	root := useWorkspaceMultiModuleFixture(t)

	var opts catalog.ExportOptions
	filter := catalog.Filter{Include: catalog.IncludeOptions{PackageDeps: true}}
	if err := attachPackageDeps(&opts, filter, root); err != nil {
		t.Fatalf("attachPackageDeps(workspace-multimodule): %v", err)
	}
	if opts.Packages == nil {
		t.Fatal("opts.Packages is nil after attachPackageDeps")
	}
	if opts.Packages.Error != "" {
		t.Fatalf("packageDeps load error: %s", opts.Packages.Error)
	}
	if opts.Packages.Graph == nil {
		t.Fatal("opts.Packages.Graph is nil")
	}
	if opts.Packages.Graph.ByID(wsmmCorePkg) == nil {
		t.Errorf("exported graph missing core package %q", wsmmCorePkg)
	}
	if opts.Packages.Graph.ByID(wsmmSatPkg) == nil {
		t.Errorf("exported graph missing satellite package %q — satellite silently dropped (the F2 bug)", wsmmSatPkg)
	}
}
