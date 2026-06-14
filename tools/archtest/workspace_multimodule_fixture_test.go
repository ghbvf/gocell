//go:build archtest

// INVARIANT: WORKSPACE-MULTIMODULE-SCAN-01
//
// The reverse fixture for #1555: proves the archtest workspace-root model loads
// AND classifies EVERY member of a real multi-module workspace, so a module
// extracted into go.work is not silently dropped from the production scan (the
// keystone). testdata/workspace-multimodule is a self-contained two-module
// workspace whose satellite module path is a string subpath of the core module
// path (the Plan D shape), exercising the longest-prefix owner selection.
package archtest

import (
	"path/filepath"
	"testing"

	kerneldepgraph "github.com/ghbvf/gocell/framework/kernel/depgraph"
)

const (
	wsmmCoreModule = "example.test/wsmm"
	wsmmSatModule  = "example.test/wsmm/satellite"
)

// TestWorkspaceMultiModuleFixture loads the testdata multi-module workspace
// through the SAME path archtest uses for the real repo (findWorkspaceModulePaths
// → LoadProductionPackages → FromPackages) and asserts:
//
//	(a) both member modules are enumerated and scanned;
//	(b) the satellite's cell package — whose path is a subpath of the core
//	    module — classifies as LayerCells within the SATELLITE module (not
//	    LayerUnknown of the core module): longest-prefix owner selection works;
//	(c) the cross-module import-direction rule sees the real satellite→core edge
//	    (flagged only when the satellite is treated as base, never the core).
func TestWorkspaceMultiModuleFixture(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace-multimodule")
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	// Point the workspace loader at the FIXTURE's go.work (the production scan
	// uses ModeWorkspace = ambient workspace; without this it would inherit the
	// repo's go.work, which does not list the fixture's modules). Not parallel:
	// t.Setenv mutates process env. The SharedResolver cache key is rooted at
	// the fixture dir, so this does not alias the repo's cached load.
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	g, _ := loadModule(t, root)

	// (a) both modules enumerated.
	if len(g.Modules) != 2 {
		t.Fatalf("g.Modules = %v, want the 2 fixture modules", g.Modules)
	}
	gotMods := map[string]bool{}
	for _, m := range g.Modules {
		gotMods[m] = true
	}
	if !gotMods[wsmmCoreModule] || !gotMods[wsmmSatModule] {
		t.Fatalf("g.Modules = %v, want both %q and %q", g.Modules, wsmmCoreModule, wsmmSatModule)
	}

	// both modules' packages scanned.
	corePkg := wsmmCoreModule + "/cells/corecell"
	satPkg := wsmmSatModule + "/cells/satcell"
	if g.ByID(corePkg) == nil {
		t.Fatalf("core package %q not in workspace scan", corePkg)
	}
	satNode := g.ByID(satPkg)
	if satNode == nil {
		t.Fatalf("satellite package %q not in workspace scan (nested module dropped — the bug #1555 fixes)", satPkg)
	}

	// (b) longest-prefix owner: satellite cell classifies within the satellite
	// module, NOT as LayerUnknown of the core module.
	if satNode.Layer != kerneldepgraph.LayerCells {
		t.Errorf("satellite cell %q Layer = %q, want %q (longest-prefix owner = satellite module)",
			satPkg, satNode.Layer, kerneldepgraph.LayerCells)
	}
	if satNode.CellID != "satcell" {
		t.Errorf("satellite cell %q CellID = %q, want %q", satPkg, satNode.CellID, "satcell")
	}

	// (c) cross-module direction on real load data:
	//   - core as base → 0 (core does not import the satellite: correct direction).
	if v := CheckCrossModuleImportDirection(g, wsmmCoreModule); len(v) != 0 {
		t.Errorf("core-as-base: want 0 violations (core must not import satellite), got %+v", v)
	}
	//   - satellite as base → the real satellite→core edge is detected, proving
	//     the rule observes genuine cross-module edges from the live load.
	v := CheckCrossModuleImportDirection(g, wsmmSatModule)
	foundEdge := false
	for _, viol := range v {
		if viol.Pkg == satPkg && viol.Import == corePkg && viol.Module == wsmmCoreModule {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Errorf("satellite-as-base: real satellite→core edge %s → %s not detected; got %+v",
			satPkg, corePkg, v)
	}
}
