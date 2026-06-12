//go:build archtest

// INVARIANT: CROSS-MODULE-IMPORT-DIRECTION-01
//
// The base (core) workspace module must not import any other workspace member
// module — satellites depend on core, never the reverse (Plan D: 顶层 = core).
//
// Scanning tool: g.Modules + kerneldepgraph.Classifier.OwningModule longest-prefix
// resolution on each real packages.Load import edge. Rated Medium (see
// CheckCrossModuleImportDirection godoc for the full AI-robust grade + the
// per-satellite depguard Hard complement).
//
// Tool-godoc blind spots (each has a reverse self-check below):
//   - A base package that references a satellite via a string-built path rather
//     than a real import edge: not modeled — depgraph edges are real
//     packages.Load Imports, so a non-importing reference creates no edge.
//     Reverse self-check: TestCrossModuleImportDirection01_SyntheticGraph asserts
//     only declared Imports become edges (a node with no Imports yields none).
//   - Direction hardcoding: the rule could trivially "pass" by always treating
//     the imported side as base. Reverse self-check: the negative control runs
//     the same graph with the OTHER module as base and asserts the violation set
//     flips — proving the direction is parameterized, not baked in.
package archtest

import (
	"testing"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// TestCrossModuleImportDirection01_RealRepoVacuous runs the rule over the live
// workspace graph and asserts ZERO core→satellite edges. The workspace already
// holds satellite modules (adapters/*, corecells, cellmodules, cmd/*, tools, and
// generated as of #1564), so this is a live gate — it stays empty only because
// the layering holds (no root-module package imports a satellite), not because
// the workspace is single-module. The firing path is exercised by
// TestCrossModuleImportDirection01_SyntheticGraph.
func TestCrossModuleImportDirection01_RealRepoVacuous(t *testing.T) {
	root := findModuleRoot(t)
	g, _ := loadModule(t, root)
	coreModule := readModulePath(t, root)
	if v := CheckCrossModuleImportDirection(g, coreModule); len(v) > 0 {
		t.Errorf("CROSS-MODULE-IMPORT-DIRECTION-01: base module %q must not import any other workspace module: %+v",
			coreModule, v)
	}
}

// TestCrossModuleImportDirection01_SyntheticGraph proves the rule fires on a
// planted base→satellite edge, leaves the allowed satellite→core direction
// alone, and is direction-parameterized (negative control). It uses a synthetic
// two-module graph so the logic is verified without a loadable fixture (the
// real loadable multi-module fixture is exercised by
// TestWorkspaceMultiModuleFixture).
func TestCrossModuleImportDirection01_SyntheticGraph(t *testing.T) {
	t.Parallel()
	const (
		core = "example.test/core"
		sat  = "example.test/satellite" // unrelated path — not a core subpath
	)
	g := kerneldepgraph.FromNodes([]string{core, sat}, []*kerneldepgraph.Node{
		// core package importing a satellite package — the forbidden direction.
		{ID: core + "/cmd/app", Imports: []string{sat + "/cells/x", core + "/kernel"}},
		// satellite package importing core — the ALLOWED direction.
		{ID: sat + "/cells/x", Imports: []string{core + "/kernel"}},
		// pure base package, no cross-module edge.
		{ID: core + "/kernel", Imports: []string{}},
	})

	v := CheckCrossModuleImportDirection(g, core)
	if len(v) != 1 {
		t.Fatalf("want exactly 1 base→satellite violation, got %d: %+v", len(v), v)
	}
	if v[0].Pkg != core+"/cmd/app" || v[0].Import != sat+"/cells/x" || v[0].Module != sat {
		t.Fatalf("violation = %+v, want core/cmd/app → satellite/cells/x (module satellite)", v[0])
	}
	// Explicit assertion: the allowed satellite→core direction (sat+"/cells/x") must NOT be flagged.
	for _, viol := range v {
		if viol.Pkg == sat+"/cells/x" {
			t.Errorf("satellite→core allowed direction must not be flagged; got violation %+v", viol)
		}
	}
	// Reverse self-check: the allowed satellite→core edge must NOT appear.
	for _, viol := range v {
		if viol.Module == core {
			t.Errorf("satellite→core is allowed and must not be flagged; got %+v", viol)
		}
	}

	// Negative control: treat the satellite as base. Now the satellite→core edge
	// is the inversion and core→satellite is allowed — the violation set flips,
	// proving the direction is a parameter, not hardcoded.
	v2 := CheckCrossModuleImportDirection(g, sat)
	if len(v2) != 1 || v2[0].Pkg != sat+"/cells/x" || v2[0].Import != core+"/kernel" || v2[0].Module != core {
		t.Fatalf("negative control: want 1 satellite→core violation when satellite is base, got %+v", v2)
	}
}
