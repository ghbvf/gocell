package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// buildAssemblyProject constructs a minimal *metadata.ProjectMeta with the given cells
// and one assembly referencing them. Used to drive ExportedApplyAssemblyDerivations
// without touching the filesystem.
func buildAssemblyProject(cellLevels map[string]string, asmCells []string, asm *metadata.AssemblyMeta) *metadata.ProjectMeta {
	pm := &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     make(map[string]*metadata.SliceMeta),
		Contracts:  make(map[string]*metadata.ContractMeta),
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
	// F8 (R3): re-validate every cell-id through the typed builder so a
	// bare-literal call site fails fast in the helper, closing the
	// AssignStmt blind spot (A1 only scans CompositeLit positions).
	for id, lvl := range cellLevels {
		c := &metadata.CellMeta{ConsistencyLevel: lvl}
		c.ID = metadatatest.NewCellID(id)
		pm.Cells[c.ID] = c
	}
	asm.Cells = asmCells
	pm.Assemblies[asm.ID] = asm
	return pm
}

func TestApplyAssemblyDerivations_BuildAllOmitted(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "testbundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	mycelll1 := metadatatest.NewCellID("mycelll1")
	pm := buildAssemblyProject(map[string]string{mycelll1: "L1"}, []string{mycelll1}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	assert.Equal(t, "cmd/testbundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "testbundle", asm.Build.Binary)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	assert.Equal(t, "L1", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_PartialBuildOverride(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "custombundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Build: metadata.BuildMeta{Binary: "custom-bin"},
	}
	somecell := metadatatest.NewCellID("somecell")
	pm := buildAssemblyProject(map[string]string{somecell: "L2"}, []string{somecell}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	// Binary must not be overridden
	assert.Equal(t, "custom-bin", asm.Build.Binary)
	// Others are derived
	assert.Equal(t, "cmd/custombundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	assert.Equal(t, "L2", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_MaxConsistencyMultipleCells(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "multibundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(
		map[string]string{
			metadatatest.CellIDCellA: "L0",
			metadatatest.CellIDCellB: "L2",
			metadatatest.CellIDCellC: "L4",
		},
		[]string{metadatatest.CellIDCellA, metadatatest.CellIDCellB, metadatatest.CellIDCellC},
		asm,
	)

	metadata.ExportedApplyAssemblyDerivations(pm)

	assert.Equal(t, "L4", asm.MaxConsistencyLevel)
}

func TestApplyAssemblyDerivations_MaxConsistencyAllL0(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "l0bundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	purecalc := metadatatest.NewCellID("purecalc")
	pm := buildAssemblyProject(map[string]string{purecalc: "L0"}, []string{purecalc}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	assert.Equal(t, "L0", asm.MaxConsistencyLevel)
}

// TestApplyAssemblyDerivations_MissingCellRefSkipsMaxLevel asserts the
// layering decision: parser-stage derivation does not validate referential
// integrity; unknown cell IDs leave MaxConsistencyLevel empty so governance
// REF-* / TOPO-09 can report the issue without being shadowed by a
// parser-level error.
func TestApplyAssemblyDerivations_MissingCellRefSkipsMaxLevel(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "badbundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{}, []string{metadatatest.NewCellID("nonexistent")}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

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
	asm := &metadata.AssemblyMeta{
		ID:    "todoorder",
		File:  "examples/todoorder/assembly.yaml",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{metadatatest.CellIDOrderCell: "L2"}, []string{metadatatest.CellIDOrderCell}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	if asm.Build.Entrypoint != "examples/todoorder/main.go" {
		t.Errorf("entrypoint: want %q, got %q", "examples/todoorder/main.go", asm.Build.Entrypoint)
	}
	if got := metadata.AssemblyGeneratedDir(asm); got != "examples/todoorder/generated" {
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
			// Post-M1 (#1082) Locator funnel: empty File is a parsing-contract
			// violation, not a recoverable fallback condition. AssemblyGeneratedDir
			// returns "" so callers detect the missing-meta condition rather than
			// silently producing a derived path that could collide with real
			// output. Scaffold (kernel/assembly.synthesizeAssemblyMeta) now sets
			// File explicitly to "assemblies/<id>/assembly.yaml".
			name:     "empty_file_returns_empty",
			asmID:    "newasm",
			file:     "",
			wantPath: "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			asm := &metadata.AssemblyMeta{ID: tc.asmID, File: tc.file}
			got := metadata.AssemblyGeneratedDir(asm)
			if got != tc.wantPath {
				t.Errorf("AssemblyGeneratedDir(%q): want %q, got %q", tc.file, tc.wantPath, got)
			}
		})
	}
}

// TestApplyAssemblyDerivations_ConventionalAssemblyEntrypoint verifies that an
// assembly under assemblies/<id>/ derives the conventional cmd/<id>/main.go
// entrypoint (not identity-by-location assemblies/<id>/main.go), so the
// conventional starter layout is preserved.
func TestApplyAssemblyDerivations_ConventionalAssemblyEntrypoint(t *testing.T) {
	t.Parallel()
	asm := &metadata.AssemblyMeta{
		ID:    "corebundle",
		File:  "assemblies/corebundle/assembly.yaml",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{}, []string{}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	if asm.Build.Entrypoint != "cmd/corebundle/main.go" {
		t.Errorf("entrypoint: want %q, got %q", "cmd/corebundle/main.go", asm.Build.Entrypoint)
	}
}

// TestApplyAssemblyDerivations_ManifestCustomEntrypoint verifies that an
// assembly in a Manifest-mode custom path (not assemblies/ or examples/)
// derives the identity-by-location entrypoint (path.Dir(file)/main.go).
func TestApplyAssemblyDerivations_ManifestCustomEntrypoint(t *testing.T) {
	t.Parallel()
	asm := &metadata.AssemblyMeta{
		ID:    "payment",
		File:  "services/payment/assembly.yaml",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	pm := buildAssemblyProject(map[string]string{}, []string{}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	if asm.Build.Entrypoint != "services/payment/main.go" {
		t.Errorf("entrypoint: want %q, got %q", "services/payment/main.go", asm.Build.Entrypoint)
	}
}

// TestIsConventionalAssemblyPath exercises all branches of the
// IsConventionalAssemblyPath classification funnel.
func TestIsConventionalAssemblyPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"assemblies/corebundle/assembly.yaml", true},
		{"assemblies/corebundle/", true},
		{"assemblies/", true},
		{"examples/todoorder/assembly.yaml", false},
		{"services/payment/assembly.yaml", false},
		{"", false},
		{"cmd/corebundle/main.go", false},
	}
	for _, tc := range cases {
		got := metadata.IsConventionalAssemblyPath(tc.path)
		if got != tc.want {
			t.Errorf("IsConventionalAssemblyPath(%q): want %v, got %v", tc.path, tc.want, got)
		}
	}
}

func TestApplyAssemblyDerivations_InvalidLevelSkipsMaxLevel(t *testing.T) {
	asm := &metadata.AssemblyMeta{
		ID:    "badlvlbundle",
		Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	}
	badlevelcell := metadatatest.NewCellID("badlevelcell")
	pm := buildAssemblyProject(map[string]string{badlevelcell: "L9"}, []string{badlevelcell}, asm)

	metadata.ExportedApplyAssemblyDerivations(pm)

	// Same layering rationale — invalid level is FMT-03 territory.
	assert.Empty(t, asm.MaxConsistencyLevel)
}
