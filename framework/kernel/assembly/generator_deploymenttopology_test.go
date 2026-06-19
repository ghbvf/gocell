package assembly

import (
	"bytes"
	"errors"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
	ecErr "github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
)

// buildCompositionProjectForTopology returns a ProjectMeta with composition form
// (CompositionAPI: true) and an assembly whose topology can be set in tests.
func buildCompositionProjectForTopology(topo metadata.TopologyMeta) *metadata.ProjectMeta {
	cells := []metadata.AssemblyCellRef{
		{ID: metadatatest.CellIDAccessCore},
		{ID: metadatatest.CellIDAuditCore},
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore},
			metadatatest.CellIDAuditCore:  {ID: metadatatest.CellIDAuditCore},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: make(map[string]*metadata.ContractMeta),
		Journeys:  make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"topobundle": {
				ID:       "topobundle",
				Build:    metadata.BuildMeta{CompositionAPI: true},
				Cells:    cells,
				Topology: topo,
			},
		},
	}
}

// TestGenerateModulesGen_TopologyGroups_Empty verifies that the composition form
// always emits generatedTopologyGroups() returning nil when the assembly has no
// topology declared (all-colocated default).
func TestGenerateModulesGen_TopologyGroups_Empty(t *testing.T) {
	project := buildCompositionProjectForTopology(metadata.TopologyMeta{})
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err)

	content := string(out)
	// ALWAYS-emit: function must be present even when topology is empty.
	assert.Contains(t, content, "func generatedTopologyGroups() []bootstrap.TopologyGroup")
	// bootstrap import must always be present (return type references it).
	assert.Contains(t, content, `"github.com/ghbvf/gocell/framework/runtime/bootstrap"`)
	// Empty topology returns nil.
	assert.Contains(t, content, "return nil")
	// No group literal emitted.
	assert.NotContains(t, content, "bootstrap.TopologyGroup{")
}

// TestGenerateModulesGen_TopologyGroups_SingleGroup_ValidGo verifies that a
// single-group topology passes the codegen gate and emits syntactically valid Go
// (F2) with the expected generatedTopologyGroups() function. Uses format.Source
// to prove the output is gofmt-parseable.
func TestGenerateModulesGen_TopologyGroups_SingleGroup_ValidGo(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "monolith", Cells: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore}, Endpoint: "mono.svc:9000"},
	}}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err, "single-group topology must not be rejected by codegen")

	formatted, fmtErr := format.Source(out)
	require.NoError(t, fmtErr, "generated Go must be parseable by go/format.Source")

	content := string(formatted)
	assert.Contains(t, content, "func generatedTopologyGroups() []bootstrap.TopologyGroup")
	assert.Contains(t, content, "[]bootstrap.TopologyGroup{")
	assert.Contains(t, content, `Role:     "monolith"`)
	assert.Contains(t, content, `"accesscore"`)
	assert.Contains(t, content, `"auditcore"`)
	assert.Contains(t, content, `Endpoint: "mono.svc:9000"`)
}

// TestGenerateModulesGen_TopologyGroups_MultiGroup_Golden is the Hard codegen
// golden: a fixed two-group partition must render byte-for-byte to the committed
// golden, so any drift in generatedTopologyGroups() rendering surfaces as a byte
// diff. Regenerate the golden by re-running this test with the package -update flag.
func TestGenerateModulesGen_TopologyGroups_MultiGroup_Golden(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{metadatatest.CellIDAccessCore}, Endpoint: "https://core.svc:9443"},
		{Role: "edge", Cells: []string{metadatatest.CellIDAuditCore}, Endpoint: "https://edge.svc:9443"},
	}}
	project := buildCompositionProjectForTopology(topo)
	// #2429: require a capability so the golden carries the runtime/capability import
	// alongside bootstrap/composition. This byte-locks the gofumpt-canonical import
	// ordering (bootstrap < capability < composition) — the exact block whose
	// hardcoded mis-ordering tripped the pre-push gofumpt gate. Without a capability
	// the import block omits capability and the regression is invisible to the golden.
	project.Cells[metadatatest.CellIDAccessCore].Requires = []string{"postgres"}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err, "multi-group topology must be rendered by codegen")

	// The emitted Go must be gofmt-parseable (so a golden update can never freeze
	// un-parseable output).
	_, fmtErr := format.Source(out)
	require.NoError(t, fmtErr, "generated Go must be parseable by go/format.Source")

	goldenPath := filepath.Join("testdata", "modules_gen_topology_groups.go.golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(goldenPath, out, 0o644))
		t.Logf("golden updated: %s", goldenPath)
		return
	}
	golden := fileutil.MustReadFile(t, goldenPath)
	if !bytes.Equal(out, golden) {
		t.Errorf("generated modules_gen.go diverges from golden:\n--- got ---\n%s\n--- want ---\n%s", out, golden)
	}
	// Belt (so a carelessly emptied golden cannot pass vacuously): the rendered
	// group literal must carry both roles, both cell IDs and both endpoints.
	for _, want := range []string{
		"[]bootstrap.TopologyGroup{", `Role:     "core"`, `Role:     "edge"`,
		`"accesscore"`, `"auditcore"`, `"https://core.svc:9443"`, `"https://edge.svc:9443"`,
		// #2429: the capability import must be present (so the golden actually
		// exercises the bootstrap < capability < composition ordering it locks).
		`"github.com/ghbvf/gocell/framework/runtime/capability"`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("generated output missing %q:\n%s", want, out)
		}
	}
	// #2429: the framework import block must be gofumpt-canonical regardless of the
	// golden bytes — bootstrap < capability < composition (alphabetical path order).
	posBootstrap := bytes.Index(out, []byte(`"github.com/ghbvf/gocell/framework/runtime/bootstrap"`))
	posCapability := bytes.Index(out, []byte(`"github.com/ghbvf/gocell/framework/runtime/capability"`))
	posComposition := bytes.Index(out, []byte(`"github.com/ghbvf/gocell/framework/runtime/composition"`))
	require.Positive(t, posBootstrap)
	require.Positive(t, posCapability)
	require.Positive(t, posComposition)
	assert.Less(t, posBootstrap, posCapability, "bootstrap import must precede capability (#2429)")
	assert.Less(t, posCapability, posComposition, "capability import must precede composition (#2429)")
}

// TestGenerateModulesGen_TopologyGroups_IllegalCellInTwoGroups is the
// synthetic-red test (F10): a cell assigned to two groups is an illegal topology
// that GenerateModulesGen must reject before emitting any output, returning an
// errcode.ErrMetadataInvalid error.
func TestGenerateModulesGen_TopologyGroups_IllegalCellInTwoGroups(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		// accesscore appears in BOTH groups — mutual exclusion violation.
		{Role: "core", Cells: []string{metadatatest.CellIDAccessCore}, Endpoint: "core.svc:9000"},
		{Role: "edge", Cells: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore}, Endpoint: "edge.svc:9001"},
	}}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateModulesGen("topobundle")
	require.Error(t, err, "illegal topology (cell in two groups) must fail generation closed")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec), "error must be an errcode.Error, got: %T", err)
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
	assert.Contains(t, strings.ToLower(err.Error()), "more than one group",
		"error message must mention the duplicate-group cause so a generic error cannot pass vacuously")
}

// TestGenerateModulesGen_TopologyGroups_IllegalNonExhaustive is a synthetic-red
// test (F10): a non-exhaustive topology (a cell assigned to no group) must fail
// generation closed.
func TestGenerateModulesGen_TopologyGroups_IllegalNonExhaustive(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		// Only accesscore assigned; auditcore missing — non-exhaustive.
		{Role: "core", Cells: []string{metadatatest.CellIDAccessCore}, Endpoint: "core.svc:9000"},
	}}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateModulesGen("topobundle")
	require.Error(t, err, "non-exhaustive topology must fail generation closed")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec), "error must be an errcode.Error, got: %T", err)
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
	assert.Contains(t, strings.ToLower(err.Error()), "non-exhaustive",
		"error message must mention 'non-exhaustive' so a generic error cannot pass vacuously")
}

// TestBuildTopologyGroupsData_Empty verifies the helper returns a zero value for
// an empty metadata.TopologyMeta.
func TestBuildTopologyGroupsData_Empty(t *testing.T) {
	got := buildTopologyGroupsData(metadata.TopologyMeta{})
	assert.Nil(t, got.Groups)
}

// TestBuildTopologyGroupsData_Populated verifies the helper faithfully copies
// each group and defensively copies the cells slice.
func TestBuildTopologyGroupsData_Populated(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{"aaa", "bbb"}, Endpoint: "core.svc:9000"},
		{Role: "edge", Cells: []string{"ccc"}, Endpoint: "edge.svc:9001"},
	}}
	got := buildTopologyGroupsData(topo)
	require.Len(t, got.Groups, 2)
	assert.Equal(t, "core", got.Groups[0].Role)
	assert.Equal(t, []string{"aaa", "bbb"}, got.Groups[0].Cells)
	assert.Equal(t, "core.svc:9000", got.Groups[0].Endpoint)
	assert.Equal(t, "edge", got.Groups[1].Role)

	// Verify defensive copy: mutating the source must not affect the data.
	topo.Groups[0].Cells[0] = "mutated"
	assert.Equal(t, "aaa", got.Groups[0].Cells[0], "buildTopologyGroupsData must copy the cells slice")
}

// TestGenerateModulesGen_CompositionForm_IncludesTopologyGroups confirms
// generatedTopologyGroups is always emitted alongside generatedProjectionSourceTopics,
// and that corebundle (no topology) renders the nil-returning form exactly once.
func TestGenerateModulesGen_CompositionForm_IncludesTopologyGroups(t *testing.T) {
	project := buildModulesTestProject()
	asm := project.Assemblies["corebundle"]
	asm.Build.CompositionAPI = true

	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")
	out, err := gen.GenerateModulesGen("corebundle")
	require.NoError(t, err)

	content := string(out)
	// Existing assertions still hold (regression guard).
	assert.Contains(t, content, "func generatedCellModules() []composition.CellModule")
	assert.Contains(t, content, "func generatedProjectionSourceTopics() []string")
	// generatedTopologyGroups must ALWAYS be emitted.
	assert.Contains(t, content, "func generatedTopologyGroups() []bootstrap.TopologyGroup")
	// bootstrap import must be present.
	assert.Contains(t, content, `"github.com/ghbvf/gocell/framework/runtime/bootstrap"`)
	// corebundle has no topology → nil.
	assert.Contains(t, content, "return nil")
	// Emitted exactly once.
	assert.Equal(t, 1, strings.Count(content, "func generatedTopologyGroups()"),
		"generatedTopologyGroups must be emitted exactly once")
}
