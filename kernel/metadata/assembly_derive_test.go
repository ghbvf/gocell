package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// buildAssemblyProject constructs a minimal *ProjectMeta with the given cells
// and one assembly referencing them. Used to drive applyAssemblyDerivations
// without touching the filesystem.
func buildAssemblyProject(cellLevels map[string]string, asmCells []string, asm *AssemblyMeta) *ProjectMeta {
	pm := &ProjectMeta{
		Cells:      make(map[string]*CellMeta),
		Slices:     make(map[string]*SliceMeta),
		Contracts:  make(map[string]*ContractMeta),
		Journeys:   make(map[string]*JourneyMeta),
		Assemblies: make(map[string]*AssemblyMeta),
		fileNodes:  nil,
	}
	for id, lvl := range cellLevels {
		pm.Cells[id] = &CellMeta{
			ID:               id,
			ConsistencyLevel: lvl,
		}
	}
	asm.Cells = asmCells
	pm.Assemblies[asm.ID] = asm
	return pm
}

func TestApplyAssemblyDerivations_BuildAllOmitted(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "testbundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{"mycelll1": "L1"}, []string{"mycelll1"}, asm)

	applyAssemblyDerivations(pm)

	assert.Equal(t, "cmd/testbundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "testbundle", asm.Build.Binary)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	assert.Equal(t, "L1", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_PartialBuildOverride(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "custombundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
		Build: BuildMeta{Binary: "custom-bin"},
	}
	pm := buildAssemblyProject(map[string]string{"somecell": "L2"}, []string{"somecell"}, asm)

	applyAssemblyDerivations(pm)

	// Binary must not be overridden
	assert.Equal(t, "custom-bin", asm.Build.Binary)
	// Others are derived
	assert.Equal(t, "cmd/custombundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	assert.Equal(t, "L2", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_MaxConsistencyMultipleCells(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "multibundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(
		map[string]string{
			"cell-a": "L0",
			"cell-b": "L2",
			"cell-c": "L4",
		},
		[]string{"cell-a", "cell-b", "cell-c"},
		asm,
	)

	applyAssemblyDerivations(pm)

	assert.Equal(t, "L4", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_MaxConsistencyAllL0(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "l0bundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{"purecalc": "L0"}, []string{"purecalc"}, asm)

	applyAssemblyDerivations(pm)

	assert.Equal(t, "L0", asm.MaxConsistencyLevel)
}

// TestApplyAssemblyDerivations_MissingCellRefSkipsMaxLevel asserts the
// layering decision: parser-stage derivation does not validate referential
// integrity; unknown cell IDs leave MaxConsistencyLevel empty so governance
// REF-* / TOPO-09 can report the issue without being shadowed by a
// parser-level error.
func TestApplyAssemblyDerivations_MissingCellRefSkipsMaxLevel(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "badbundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{}, []string{"nonexistent"}, asm)

	applyAssemblyDerivations(pm)

	// Build defaults are still applied even when cells are unresolvable.
	assert.Equal(t, "cmd/badbundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "badbundle", asm.Build.Binary)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	// MaxConsistencyLevel left at zero value; governance reports the unknown ref.
	assert.Empty(t, asm.MaxConsistencyLevel)
}

// TestApplyAssemblyDerivations_ExamplesEntrypoint verifies that assemblies
// under examples/ derive their entrypoint and generated dir from the examples/
// prefix — used by the generator, governance REF-16, and generate commands.
func TestApplyAssemblyDerivations_ExamplesEntrypoint(t *testing.T) {
	t.Parallel()
	asm := &AssemblyMeta{
		ID:    "todoorder",
		File:  "examples/todoorder/assembly.yaml",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{"ordercell": "L2"}, []string{"ordercell"}, asm)

	applyAssemblyDerivations(pm)

	if asm.Build.Entrypoint != "examples/todoorder/main.go" {
		t.Errorf("entrypoint: want %q, got %q", "examples/todoorder/main.go", asm.Build.Entrypoint)
	}
	if got := AssemblyGeneratedDir(asm); got != "examples/todoorder/generated" {
		t.Errorf("generated dir: want %q, got %q", "examples/todoorder/generated", got)
	}
}

// TestAssemblyGeneratedDir verifies the derivation helper used by generator,
// governance validateREF16, and generate commands.
func TestAssemblyGeneratedDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		asmID    string
		file     string
		wantPath string
	}{
		{
			name:     "examples_prefix",
			asmID:    "todoorder",
			file:     "examples/todoorder/assembly.yaml",
			wantPath: "examples/todoorder/generated",
		},
		{
			name:     "assemblies_prefix",
			asmID:    "corebundle",
			file:     "assemblies/corebundle/assembly.yaml",
			wantPath: "assemblies/corebundle/generated",
		},
		{
			name:     "empty_file_defaults_to_assemblies",
			asmID:    "newasm",
			file:     "",
			wantPath: "assemblies/newasm/generated",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			asm := &AssemblyMeta{ID: tc.asmID, File: tc.file}
			got := AssemblyGeneratedDir(asm)
			if got != tc.wantPath {
				t.Errorf("AssemblyGeneratedDir(%q): want %q, got %q", tc.file, tc.wantPath, got)
			}
		})
	}
}

func TestApplyAssemblyDerivations_InvalidLevelSkipsMaxLevel(t *testing.T) {
	asm := &AssemblyMeta{
		ID:    "badlvlbundle",
		Owner: OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{"bad-level-cell": "L9"}, []string{"bad-level-cell"}, asm)

	applyAssemblyDerivations(pm)

	// Same layering rationale — invalid level is FMT-03 territory.
	assert.Empty(t, asm.MaxConsistencyLevel)
}
