package assembly

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

const acmePayModule = "github.com/acme/payment-cell"

// zzzExtraModule sorts AFTER github.com/ghbvf/gocell/framework/runtime/* (z > g),
// so its cellmodule import must render after the framework imports — the case the
// acme fixture (acme < ghbvf) cannot exercise. Used by #2429's sort-order test.
const zzzExtraModule = "github.com/zzz/extra-cell"

// buildCrossModuleProject builds a compositionAPI assembly that mixes a
// same-module cell (configcore) and a cross-module cell (payment, sourced from
// acmePayModule). Both cells are present in ProjectMeta.Cells so the generator
// can resolve them (workspace-mode discovery, #1086).
func buildCrossModuleProject(compositionAPI bool) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		// GoStructName is set so the legacy-form path (compositionAPI=false) gets
		// past the same-module configcore cell and reaches the cross-module payment
		// ref — where the "requires compositionAPI" guard fires. The composition
		// form ignores GoStructName.
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConfigCore:     {ID: metadatatest.CellIDConfigCore, GoStructName: metadata.MustNewGoIdentifier("ConfigCore")},
			metadatatest.NewCellID("payment"): {ID: metadatatest.NewCellID("payment"), GoStructName: metadata.MustNewGoIdentifier("Payment")},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: make(map[string]*metadata.ContractMeta),
		Journeys:  make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"mixedbundle": {
				ID: "mixedbundle",
				Cells: []metadata.AssemblyCellRef{
					{ID: metadatatest.CellIDConfigCore},
					{ID: metadatatest.NewCellID("payment"), Module: acmePayModule},
				},
				Build: metadata.BuildMeta{CompositionAPI: compositionAPI},
				File:  "assemblies/mixedbundle/assembly.yaml",
			},
		},
	}
}

// TestGenerateModulesGen_CrossModuleImportPath verifies the per-cell module
// funnel (#1086): a same-module cell renders <currentModule>/cellmodules/<id>
// while a cross-module cell renders <declaredModule>/cellmodules/<id>.
func TestGenerateModulesGen_CrossModuleImportPath(t *testing.T) {
	const currentModule = "github.com/ghbvf/gocell"
	gen := NewGenerator(buildCrossModuleProject(true), currentModule, "")

	out, err := gen.GenerateModulesGen("mixedbundle")
	require.NoError(t, err)
	content := string(out)

	assert.Contains(t, content, currentModule+"/cellmodules/configcore",
		"same-module cell must import from the assembly's own module")
	assert.Contains(t, content, acmePayModule+"/cellmodules/payment",
		"cross-module cell must import from its declared module")
	assert.NotContains(t, content, currentModule+"/cellmodules/payment",
		"cross-module cell must NOT fall back to the current module")
}

// TestGenerateModulesGen_ExplicitSameModuleIsNotCrossModule verifies that a cell
// declaring `module: <the assembly's own module>` explicitly renders identically
// to the omitted-module form (moduleOf treats module == current as same-module).
func TestGenerateModulesGen_ExplicitSameModuleIsNotCrossModule(t *testing.T) {
	const currentModule = "github.com/ghbvf/gocell"
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConfigCore: {ID: metadatatest.CellIDConfigCore},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: make(map[string]*metadata.ContractMeta),
		Journeys:  make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"explicitsame": {
				ID:    "explicitsame",
				Cells: []metadata.AssemblyCellRef{{ID: metadatatest.CellIDConfigCore, Module: currentModule}},
				Build: metadata.BuildMeta{CompositionAPI: true},
				File:  "assemblies/explicitsame/assembly.yaml",
			},
		},
	}
	out, err := NewGenerator(project, currentModule, "").GenerateModulesGen("explicitsame")
	require.NoError(t, err)
	assert.Contains(t, string(out), currentModule+"/cellmodules/configcore",
		"module == current module must render the same import as an omitted module")
}

// TestGenerateModulesGen_CrossModuleRequiresCompositionAPI verifies that a
// cross-module cell in a legacy (non-compositionAPI) assembly fail-fasts: the
// legacy local-CellModule-type form has no import path and cannot express a
// foreign module.
func TestGenerateModulesGen_CrossModuleRequiresCompositionAPI(t *testing.T) {
	gen := NewGenerator(buildCrossModuleProject(false), "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateModulesGen("mixedbundle")
	require.Error(t, err)
	// The cross-module guard is the only generateModulesGenLegacy error that
	// carries module= in its internal context (the unknown-cell / GoStructName
	// errors omit it), so these markers uniquely identify it.
	assert.Contains(t, err.Error(), "payment")
	assert.Contains(t, err.Error(), "module="+`"`+acmePayModule+`"`)
}

// TestModuleOfAndImportPath unit-tests the per-cell module resolution funnel
// directly (moduleOf / cellModuleImportPath / isCrossModule).
func TestModuleOfAndImportPath(t *testing.T) {
	g := &Generator{module: "github.com/ghbvf/gocell"}

	same := metadata.AssemblyCellRef{ID: metadatatest.NewCellID("configcore")}
	cross := metadata.AssemblyCellRef{ID: metadatatest.NewCellID("payment"), Module: acmePayModule}
	explicitSame := metadata.AssemblyCellRef{ID: metadatatest.NewCellID("configcore"), Module: "github.com/ghbvf/gocell"}

	assert.Equal(t, "github.com/ghbvf/gocell", g.moduleOf(same))
	assert.Equal(t, acmePayModule, g.moduleOf(cross))
	assert.Equal(t, "github.com/ghbvf/gocell", g.moduleOf(explicitSame))

	assert.False(t, g.isCrossModule(same))
	assert.True(t, g.isCrossModule(cross))
	assert.False(t, g.isCrossModule(explicitSame), "module == current module is not cross-module")

	assert.Equal(t, "github.com/ghbvf/gocell/cellmodules/configcore",
		cellModuleImportPath(g.moduleOf(same), same.ID))
	assert.Equal(t, acmePayModule+"/cellmodules/payment",
		cellModuleImportPath(g.moduleOf(cross), cross.ID))
}

// TestGenerateModulesGen_CrossModuleImportPathSortOrder is #2429's anti-vacuity
// guard for the WHOLE-block path sort: compositionImportLines must order the
// import block by import PATH, including a cross-module cellmodule path that sorts
// AFTER the framework runtime imports. The acme fixture (github.com/acme/... <
// github.com/ghbvf/...) only exercises a cellmodule that sorts BEFORE framework, so
// it cannot prove the after-framework case the helper's godoc relies on. Against
// the pre-#2429 generator (cellmodule imports sorted as one leading block, then
// framework imports hardcoded last) the zzz cellmodule would render BEFORE
// composition — so this test is genuinely red there.
func TestGenerateModulesGen_CrossModuleImportPathSortOrder(t *testing.T) {
	const currentModule = "github.com/ghbvf/gocell"
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConfigCore:   {ID: metadatatest.CellIDConfigCore},
			metadatatest.NewCellID("extra"): {ID: metadatatest.NewCellID("extra")},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: make(map[string]*metadata.ContractMeta),
		Journeys:  make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"zzzbundle": {
				ID: "zzzbundle",
				Cells: []metadata.AssemblyCellRef{
					{ID: metadatatest.CellIDConfigCore},
					{ID: metadatatest.NewCellID("extra"), Module: zzzExtraModule},
				},
				Build: metadata.BuildMeta{CompositionAPI: true},
				File:  "assemblies/zzzbundle/assembly.yaml",
			},
		},
	}
	// Require a capability so all three framework imports are present — the sort
	// must interleave bootstrap/capability/composition AND the after-framework
	// cross-module cellmodule into one ascending block.
	project.Cells[metadatatest.CellIDConfigCore].Requires = []string{"postgres"}

	out, err := NewGenerator(project, currentModule, "").GenerateModulesGen("zzzbundle")
	require.NoError(t, err)
	content := string(out)

	posComposition := indexOfStr(content, `"github.com/ghbvf/gocell/framework/runtime/composition"`)
	posZzz := indexOfStr(content, zzzExtraModule+"/cellmodules/extra")
	require.GreaterOrEqual(t, posComposition, 0, "composition import must be present")
	require.GreaterOrEqual(t, posZzz, 0, "cross-module extra import must be present")
	assert.Less(t, posComposition, posZzz,
		"a cross-module cellmodule path sorting after framework/* must render AFTER composition (#2429 whole-block path sort)")

	// Stronger anti-vacuity teeth: the entire import block must be in strict
	// ascending import-path order — this also re-catches the original
	// capability-before-bootstrap defect.
	assertImportBlockSorted(t, content)
}

// assertImportBlockSorted extracts the single import(...) block from generated Go
// and asserts the import paths are in ascending order (gofumpt's ordering for a
// single import group). Each line is either `alias "path"` or bare `"path"`; the
// path is the quoted segment.
func assertImportBlockSorted(t *testing.T, content string) {
	t.Helper()
	open := indexOfStr(content, "import (")
	require.GreaterOrEqual(t, open, 0, "generated file must have an import block")
	rest := content[open+len("import ("):]
	closeIdx := strings.IndexByte(rest, ')')
	require.GreaterOrEqual(t, closeIdx, 0, "import block must be closed")
	var paths []string
	for _, line := range strings.Split(rest[:closeIdx], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		q1 := strings.IndexByte(line, '"')
		q2 := strings.LastIndexByte(line, '"')
		require.True(t, q1 >= 0 && q2 > q1, "import line must carry a quoted path: %q", line)
		paths = append(paths, line[q1+1:q2])
	}
	require.NotEmpty(t, paths, "import block must list at least one import")
	assert.True(t, sort.StringsAreSorted(paths),
		"import block must be in ascending path order (gofumpt-canonical), got: %v", paths)
}
