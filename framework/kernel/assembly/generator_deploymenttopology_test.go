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
// golden: a fixed THREE-group partition must render byte-for-byte to the committed
// golden, so any drift in generatedTopologyGroups() rendering surfaces as a byte
// diff. The fixture exercises BOTH rendered states of the codegen-derived
// RequiresBrokerForCrossProcessEvents signal (#2196): one active amqp event
// crosses core↔edge (both → true) while standalone has no cross-process event
// (→ false). Regenerate the golden by re-running this test with the -update flag.
func TestGenerateModulesGen_TopologyGroups_MultiGroup_Golden(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore},
			metadatatest.CellIDAuditCore:  {ID: metadatatest.CellIDAuditCore},
			metadatatest.CellIDConfigCore: {ID: metadatatest.CellIDConfigCore},
		},
		Slices:   map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.session.created.v1": {
				ID: "event.session.created.v1", Kind: "event", Lifecycle: "active",
				Transports: []string{"amqp"},
				Endpoints: metadata.EndpointsMeta{
					Publisher:   metadatatest.CellIDAccessCore,
					Subscribers: []string{metadatatest.CellIDAuditCore},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"topobundle": {
				ID:    "topobundle",
				Build: metadata.BuildMeta{CompositionAPI: true},
				Cells: []metadata.AssemblyCellRef{
					{ID: metadatatest.CellIDAccessCore},
					{ID: metadatatest.CellIDAuditCore},
					{ID: metadatatest.CellIDConfigCore},
				},
				Topology: metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{metadatatest.CellIDAccessCore}, Endpoint: "https://core.svc:9443"},
					{Role: "edge", Cells: []string{metadatatest.CellIDAuditCore}, Endpoint: "https://edge.svc:9443"},
					{Role: "standalone", Cells: []string{metadatatest.CellIDConfigCore}, Endpoint: "https://standalone.svc:9443"},
				}},
			},
		},
	}
	// Slices make accesscore a publisher and auditcore a subscriber of the amqp
	// event (also drives generatedBrokerCells, keeping the golden self-consistent).
	addBrokerTestSlice(project, "sessionlogin", metadatatest.CellIDAccessCore, "event.session.created.v1", "publish")
	addBrokerTestSlice(project, "auditappend", metadatatest.CellIDAuditCore, "event.session.created.v1", "subscribe")

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
	// Belt (alignment-independent so a carelessly emptied golden cannot pass
	// vacuously): all three roles, cell IDs, endpoints, and BOTH broker states.
	for _, want := range []string{
		"[]bootstrap.TopologyGroup{",
		`"core"`, `"edge"`, `"standalone"`,
		`"accesscore"`, `"auditcore"`, `"configcore"`,
		`"https://core.svc:9443"`, `"https://edge.svc:9443"`, `"https://standalone.svc:9443"`,
		"RequiresBrokerForCrossProcessEvents: true",
		"RequiresBrokerForCrossProcessEvents: false",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("generated output missing %q:\n%s", want, out)
		}
	}
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
	got := buildTopologyGroupsData(metadata.TopologyMeta{}, nil)
	assert.Nil(t, got.Groups)
}

// TestBuildTopologyGroupsData_Populated verifies the helper faithfully copies
// each group, defensively copies the cells slice, and stamps the per-role
// RequiresBrokerForCrossProcessEvents bool from the brokerRoles set (#2196).
func TestBuildTopologyGroupsData_Populated(t *testing.T) {
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{"aaa", "bbb"}, Endpoint: "core.svc:9000"},
		{Role: "edge", Cells: []string{"ccc"}, Endpoint: "edge.svc:9001"},
	}}
	// Only "core" is a cross-process broker endpoint; "edge" is sync-only.
	brokerRoles := map[string]struct{}{"core": {}}
	got := buildTopologyGroupsData(topo, brokerRoles)
	require.Len(t, got.Groups, 2)
	assert.Equal(t, "core", got.Groups[0].Role)
	assert.Equal(t, []string{"aaa", "bbb"}, got.Groups[0].Cells)
	assert.Equal(t, "core.svc:9000", got.Groups[0].Endpoint)
	assert.True(t, got.Groups[0].RequiresBrokerForCrossProcessEvents, "core is a cross-process broker endpoint")
	assert.Equal(t, "edge", got.Groups[1].Role)
	assert.False(t, got.Groups[1].RequiresBrokerForCrossProcessEvents, "edge is sync-only")

	// Verify defensive copy: mutating the source must not affect the data.
	topo.Groups[0].Cells[0] = "mutated"
	assert.Equal(t, "aaa", got.Groups[0].Cells[0], "buildTopologyGroupsData must copy the cells slice")
}

// ---------------------------------------------------------------------------
// collectCrossProcessBrokerEventRoles — codegen-derived per-role broker signal
// (#2196). Mirrors the BrokerCells matrix posture: skip unknown/inactive, amqp-
// only, empty-transports fail-closed, set-dedup, anti-vacuity colocated GREEN.
// ---------------------------------------------------------------------------

// crossProcessTestProject builds a composition-form project whose single
// assembly "topobundle" has the given topology groups and contracts. Cells are
// derived from group membership.
func crossProcessTestProject(groups []metadata.TopologyGroup, contracts map[string]*metadata.ContractMeta) *metadata.ProjectMeta {
	cells := map[string]*metadata.CellMeta{}
	var cellRefs []metadata.AssemblyCellRef
	for _, g := range groups {
		for _, c := range g.Cells {
			if _, ok := cells[c]; ok {
				continue
			}
			cells[c] = &metadata.CellMeta{ID: c}
			cellRefs = append(cellRefs, metadata.AssemblyCellRef{ID: c})
		}
	}
	if contracts == nil {
		contracts = map[string]*metadata.ContractMeta{}
	}
	return &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: contracts,
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"topobundle": {
				ID:       "topobundle",
				Build:    metadata.BuildMeta{CompositionAPI: true},
				Cells:    cellRefs,
				Topology: metadata.TopologyMeta{Groups: groups},
			},
		},
	}
}

// xpEvent builds an event ContractMeta with explicit publisher/subscriber
// endpoints. The generator reads c.Endpoints directly (it does not run the
// parser's deriveEventSubscribers), so the test sets them by hand.
func xpEvent(id, lifecycle string, transports []string, publisher string, subscribers ...string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:         id,
		Kind:       "event",
		Lifecycle:  lifecycle,
		Transports: transports,
		Endpoints:  metadata.EndpointsMeta{Publisher: publisher, Subscribers: subscribers},
	}
}

func assertRoleSet(t *testing.T, got map[string]struct{}, want ...string) {
	t.Helper()
	require.Len(t, got, len(want), "role set size mismatch: got %v want %v", got, want)
	for _, r := range want {
		if _, ok := got[r]; !ok {
			t.Errorf("expected role %q in set, got %v", r, got)
		}
	}
}

// TestCollectCrossProcessBrokerEventRoles locks the precise per-role signal that
// drives the bootstrap broker-mandatory gate (replacing static TOPO-13 + the
// coarse HasRemoteCells proxy, #2196).
func TestCollectCrossProcessBrokerEventRoles(t *testing.T) {
	amqp := []string{"amqp"}
	core := metadata.TopologyGroup{Role: "core", Cells: []string{"accesscore"}, Endpoint: "core.svc:9000"}
	edge := metadata.TopologyGroup{Role: "edge", Cells: []string{"auditcore"}, Endpoint: "edge.svc:9000"}
	extra := metadata.TopologyGroup{Role: "extra", Cells: []string{"configcore"}, Endpoint: "extra.svc:9000"}

	cases := []struct {
		name      string
		groups    []metadata.TopologyGroup
		contracts map[string]*metadata.ContractMeta
		wantRoles []string
		wantErr   bool
	}{
		{
			name:      "empty topology → none (no process boundary)",
			groups:    nil,
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "auditcore")},
		},
		{
			name:      "single group → none (<2 groups)",
			groups:    []metadata.TopologyGroup{{Role: "mono", Cells: []string{"accesscore", "auditcore"}, Endpoint: "mono.svc:9000"}},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "auditcore")},
		},
		{
			name:      "cross-process active amqp event → both endpoint roles",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "auditcore")},
			wantRoles: []string{"core", "edge"},
		},
		{
			name:   "http-kind contract crossing groups → none (not event)",
			groups: []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"http.x.v1": {
				ID: "http.x.v1", Kind: "http", Lifecycle: "active", Transports: []string{"http"},
				Endpoints: metadata.EndpointsMeta{Server: "accesscore", Clients: []string{"auditcore"}},
			}},
		},
		{
			name:      "non-amqp event crossing groups → none (sync transport)",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", []string{"http"}, "accesscore", "auditcore")},
		},
		{
			name:      "draft event crossing groups → none (not active)",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "draft", amqp, "accesscore", "auditcore")},
		},
		{
			name:      "colocated event (same group) → none (anti-vacuity GREEN, ≥2 groups)",
			groups:    []metadata.TopologyGroup{{Role: "core", Cells: []string{"accesscore", "auditcore"}, Endpoint: "core.svc:9000"}, extra},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "auditcore")},
		},
		{
			name:      "external-actor publisher (not a cell) → none",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "externalsystem", "auditcore")},
		},
		{
			name:      "_framework sentinel publisher → none (not in any group)",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "_framework", "auditcore")},
		},
		{
			name:      "external subscriber skipped, cell subscriber counts",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "externalsystem", "auditcore")},
			wantRoles: []string{"core", "edge"},
		},
		{
			name:      "fan-out to multiple groups → all involved roles",
			groups:    []metadata.TopologyGroup{core, edge, extra},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", amqp, "accesscore", "auditcore", "configcore")},
			wantRoles: []string{"core", "edge", "extra"},
		},
		{
			name:      "registered event with empty transports → fail-closed error",
			groups:    []metadata.TopologyGroup{core, edge},
			contracts: map[string]*metadata.ContractMeta{"event.x.v1": xpEvent("event.x.v1", "active", []string{}, "accesscore", "auditcore")},
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := crossProcessTestProject(tc.groups, tc.contracts)
			gen := NewGenerator(project, "github.com/ghbvf/gocell", "")
			got, err := gen.collectCrossProcessBrokerEventRoles(project.Assemblies["topobundle"])
			if tc.wantErr {
				require.Error(t, err, "expected fail-closed error")
				var ec *ecErr.Error
				require.True(t, errors.As(err, &ec), "error must be errcode.Error, got %T", err)
				assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
				assert.Contains(t, strings.ToLower(ec.Message), "transports", "error must name the empty-transports cause")
				return
			}
			require.NoError(t, err)
			assertRoleSet(t, got, tc.wantRoles...)
		})
	}
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
