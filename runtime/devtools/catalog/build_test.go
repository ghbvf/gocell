// Package catalog_test — build_test.go: TDD tests for BuildDocument.
package catalog_test

import (
	"flag"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

var update = flag.Bool("update", false, "update golden files")

// fixedTime is an anchored timestamp for deterministic test output.
var fixedTime = time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

// fixedClock returns a FakeClock frozen at fixedTime.
func fixedClock() *clockmock.FakeClock {
	return clockmock.New(fixedTime)
}

// ---- fixture helpers ----

// minimalPM returns a ProjectMeta with 1 cell + 1 slice + 1 contract.
func minimalPM() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "access.schema.json"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.login"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:               "sessionlogin",
				BelongsToCell:    "accesscore",
				ConsistencyLevel: "L1",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.access.login.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.access.login.v1": {
				ID:        "http.access.login.v1",
				Kind:      "http",
				OwnerCell: "accesscore",
				Lifecycle: "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"webclient"},
				},
				File: "contracts/http/access/login/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
}

// fullPM returns a ProjectMeta with 2 cells + 2 slices + 2 contracts + 1 journey + 1 assembly + 1 actor.
func fullPM() *metadata.ProjectMeta {
	pm := minimalPM()
	pm.Cells["auditcore"] = &metadata.CellMeta{
		ID:               "auditcore",
		Type:             "support",
		ConsistencyLevel: "L2",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
		Schema:           metadata.SchemaMeta{Primary: "audit.schema.json"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.auditcore.write"}},
	}
	pm.Slices["auditcore/auditwrite"] = &metadata.SliceMeta{
		ID:               "auditwrite",
		BelongsToCell:    "auditcore",
		ConsistencyLevel: "L2",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "event.audit.entry.v1", Role: "publish"},
		},
	}
	pm.Contracts["event.audit.entry.v1"] = &metadata.ContractMeta{
		ID:        "event.audit.entry.v1",
		Kind:      "event",
		OwnerCell: "auditcore",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:   "auditcore",
			Subscribers: []string{"accesscore"},
		},
		File: "contracts/event/audit/entry/v1/contract.yaml",
	}
	pm.Journeys["J-001"] = &metadata.JourneyMeta{
		ID:        "J-001",
		Goal:      "User can login",
		Lifecycle: "active",
		Owner:     metadata.OwnerMeta{Team: "platform", Role: "owner"},
		Cells:     []string{"accesscore"},
		Contracts: []string{"http.access.login.v1"},
		PassCriteria: []metadata.PassCriterion{
			{Text: "Login succeeds", Mode: "auto"},
		},
		File: "journeys/J-001.yaml",
	}
	pm.Assemblies["mainbundle"] = &metadata.AssemblyMeta{
		ID:    "mainbundle",
		Cells: []string{"accesscore", "auditcore"},
		Build: metadata.BuildMeta{Entrypoint: "cmd/server/main.go", Binary: "gocell-server"},
	}
	pm.Actors = []metadata.ActorMeta{
		{ID: "webclient", MaxConsistencyLevel: "L0"},
	}
	pm.StatusBoard = []metadata.StatusBoardEntry{
		{JourneyID: "J-001", State: "green", Risk: "low", UpdatedAt: "2026-05-03"},
	}
	return pm
}

// baseOpts returns a baseline ExportOptions.
func baseOpts() catalog.ExportOptions {
	return catalog.ExportOptions{
		Clock:  fixedClock(),
		Root:   "/projects/gocell",
		Filter: catalog.Filter{Include: catalog.AllIncluded()},
	}
}

// ---- TestBuildDocument_FullSnapshot ----

func TestBuildDocument_FullSnapshot(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	assert.Equal(t, "v1", doc.SchemaVersion)
	assert.Equal(t, "gocell.io/v1alpha1", doc.APIVersion)
	assert.Equal(t, fixedTime.UTC().Format(time.RFC3339), doc.GeneratedAt)
	assert.Equal(t, "/projects/gocell", doc.Root)

	// With AllIncluded, statusBoard should be present.
	assert.NotEmpty(t, doc.StatusBoard)

	// Entities: 2 cells + 2 slices + 2 contracts + 1 journey + 1 assembly + 1 actor = 9
	assert.Len(t, doc.Entities, 9, "expected 9 entities")

	// Entities sorted by (kind, name)
	for i := 1; i < len(doc.Entities); i++ {
		prev := doc.Entities[i-1]
		curr := doc.Entities[i]
		prevKey := prev.Kind + "/" + prev.Metadata.Name
		currKey := curr.Kind + "/" + curr.Metadata.Name
		assert.LessOrEqual(t, prevKey, currKey, "entities must be sorted by (kind, name)")
	}

	// Kind-specific Spec type assertions
	kindSeen := map[string]bool{}
	for _, e := range doc.Entities {
		switch e.Kind {
		case "Cell":
			_, ok := e.Spec.(catalog.CellSpec)
			assert.True(t, ok, "Cell entity should have CellSpec, got %T", e.Spec)
		case "Slice":
			_, ok := e.Spec.(catalog.SliceSpec)
			assert.True(t, ok, "Slice entity should have SliceSpec, got %T", e.Spec)
		case "Contract":
			_, ok := e.Spec.(catalog.ContractSpec)
			assert.True(t, ok, "Contract entity should have ContractSpec, got %T", e.Spec)
		case "Journey":
			_, ok := e.Spec.(catalog.JourneySpec)
			assert.True(t, ok, "Journey entity should have JourneySpec, got %T", e.Spec)
		case "Assembly":
			_, ok := e.Spec.(catalog.AssemblySpec)
			assert.True(t, ok, "Assembly entity should have AssemblySpec, got %T", e.Spec)
		case "Actor":
			_, ok := e.Spec.(catalog.ActorSpec)
			assert.True(t, ok, "Actor entity should have ActorSpec, got %T", e.Spec)
		}
		kindSeen[e.Kind] = true
	}
	assert.True(t, kindSeen["Cell"], "should have Cell entities")
	assert.True(t, kindSeen["Slice"], "should have Slice entities")
	assert.True(t, kindSeen["Contract"], "should have Contract entities")
	assert.True(t, kindSeen["Journey"], "should have Journey entities")
	assert.True(t, kindSeen["Assembly"], "should have Assembly entities")
	assert.True(t, kindSeen["Actor"], "should have Actor entities")

	// Dependencies nil when not provided via opts
	assert.Nil(t, doc.Dependencies, "Dependencies should be nil when opts.CellDeps and opts.Packages are nil")
}

// ---- TestBuildDocument_FilterKinds ----

func TestBuildDocument_FilterKinds(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()
	opts.Filter.Kinds = []string{"Cell", "Contract"}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	for _, e := range doc.Entities {
		assert.Contains(t, []string{"Cell", "Contract"}, e.Kind,
			"only Cell and Contract should appear, got %q", e.Kind)
	}
	// 2 cells + 2 contracts = 4
	assert.Len(t, doc.Entities, 4)
}

// ---- TestBuildDocument_FilterLayers ----

func TestBuildDocument_FilterLayers(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()
	opts.Filter.Layers = []string{"cells"}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	for _, e := range doc.Entities {
		assert.Contains(t, []string{"Cell", "Slice"}, e.Kind,
			"Layers=[cells] should only include Cell and Slice, got %q", e.Kind)
	}
	// 2 cells + 2 slices = 4
	assert.Len(t, doc.Entities, 4)
}

// ---- TestBuildDocument_FilterCellsFocus ----

func TestBuildDocument_FilterCellsFocus(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()
	opts.Filter.Cells = []string{"accesscore"}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	names := make([]string, 0, len(doc.Entities))
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}

	assert.Contains(t, names, "Cell/accesscore", "focus cell must be present")
	assert.Contains(t, names, "Slice/sessionlogin", "slices of focus cell must be present")
	assert.Contains(t, names, "Contract/http.access.login.v1", "contracts of focus cell must be present")

	// auditcore should NOT appear (not a direct neighbor)
	for _, n := range names {
		assert.NotEqual(t, "Cell/auditcore", n, "auditcore should not appear when focusing on accesscore")
	}
}

// ---- TestBuildDocument_FilterCellsFocus_ConsumerContracts ----

// TestBuildDocument_FilterCellsFocus_ConsumerContracts verifies that when a
// focus cell does NOT own a contract but consumes it via contractUsages, the
// contract is still included in the filtered output (consumer-side match).
func TestBuildDocument_FilterCellsFocus_ConsumerContracts(t *testing.T) {
	pm := fullPM()
	pm.Slices["accesscore/sessionlogin"].ContractUsages = append(
		pm.Slices["accesscore/sessionlogin"].ContractUsages,
		metadata.ContractUsage{Contract: "event.audit.entry.v1", Role: "subscribe"},
	)

	opts := baseOpts()
	opts.Filter.Cells = []string{"accesscore"}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	names := make([]string, 0, len(doc.Entities))
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}

	assert.Contains(t, names, "Cell/accesscore")
	assert.Contains(t, names, "Slice/sessionlogin")
	assert.Contains(t, names, "Contract/http.access.login.v1")
	assert.Contains(t, names, "Contract/event.audit.entry.v1",
		"contract consumed by focus cell must appear even when owned by another cell")
	assert.Contains(t, names, "Cell/auditcore",
		"auditcore must appear: it owns a contract consumed by the focus cell")
}

// ---- TestBuildDocument_IncludeOptions ----

func TestBuildDocument_IncludeOptions(t *testing.T) {
	cellDeps := &catalog.CellDepGraph{
		Nodes: []string{"accesscore"},
		Edges: []catalog.CellEdge{},
	}
	pkgs := &catalog.PackageDepsView{
		Graph: &kerneldepgraph.Graph{},
	}
	pm := fullPM()

	cases := []struct {
		name          string
		inc           catalog.IncludeOptions
		wantRelations bool
		wantStatus    bool
		wantCellDeps  bool
		wantPkgDeps   bool
	}{
		{
			name:          "Relations only",
			inc:           catalog.IncludeOptions{Relations: true},
			wantRelations: true,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "StatusBoard only",
			inc:           catalog.IncludeOptions{StatusBoard: true},
			wantRelations: false,
			wantStatus:    true,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "CellDeps only",
			inc:           catalog.IncludeOptions{CellDeps: true},
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  true,
			wantPkgDeps:   false,
		},
		{
			name:          "PackageDeps only",
			inc:           catalog.IncludeOptions{PackageDeps: true},
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   true,
		},
		{
			name:          "AllIncluded",
			inc:           catalog.AllIncluded(),
			wantRelations: true,
			wantStatus:    true,
			wantCellDeps:  true,
			wantPkgDeps:   true,
		},
		{
			name:          "zero IncludeOptions",
			inc:           catalog.IncludeOptions{},
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "Relations+CellDeps",
			inc:           catalog.IncludeOptions{Relations: true, CellDeps: true},
			wantRelations: true,
			wantStatus:    false,
			wantCellDeps:  true,
			wantPkgDeps:   false,
		},
		{
			name:          "StatusBoard+PackageDeps",
			inc:           catalog.IncludeOptions{StatusBoard: true, PackageDeps: true},
			wantRelations: false,
			wantStatus:    true,
			wantCellDeps:  false,
			wantPkgDeps:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := catalog.ExportOptions{
				Clock:    fixedClock(),
				Filter:   catalog.Filter{Include: tc.inc},
				CellDeps: cellDeps,
				Packages: pkgs,
			}
			doc, err := catalog.BuildDocument(pm, opts)
			require.NoError(t, err)
			assertIncludeFlags(t, doc, tc.wantRelations, tc.wantStatus, tc.wantCellDeps, tc.wantPkgDeps)
		})
	}
}

// assertIncludeFlags verifies presence/absence of the four optional document
// blocks driven by IncludeOptions. Extracted from TestBuildDocument_IncludeOptions
// to keep the loop body's cognitive complexity below the project ceiling.
func assertIncludeFlags(t *testing.T, doc catalog.Document, wantRelations, wantStatus, wantCellDeps, wantPkgDeps bool) {
	t.Helper()

	hasRelations := false
	for _, e := range doc.Entities {
		if len(e.Relations) > 0 {
			hasRelations = true
			break
		}
	}
	assert.Equal(t, wantRelations, hasRelations, "relations presence mismatch")

	if wantStatus {
		assert.NotEmpty(t, doc.StatusBoard, "expected statusBoard to be present")
	} else {
		assert.Empty(t, doc.StatusBoard, "expected statusBoard to be absent")
	}

	assertDepBlock(t, doc, wantCellDeps, func(d *catalog.Dependencies) any { return d.Cells }, "Dependencies.Cells")
	assertDepBlock(t, doc, wantPkgDeps, func(d *catalog.Dependencies) any { return d.Packages }, "Dependencies.Packages")
}

// assertDepBlock checks a single sub-block of doc.Dependencies, picked by
// `pick`. When `want` is true the sub-block must be non-nil; otherwise it
// must be nil (or the parent Dependencies may be absent entirely).
func assertDepBlock(t *testing.T, doc catalog.Document, want bool, pick func(*catalog.Dependencies) any, label string) {
	t.Helper()
	if want {
		require.NotNil(t, doc.Dependencies, "expected Dependencies block for %s", label)
		assert.NotNil(t, pick(doc.Dependencies), "expected %s", label)
		return
	}
	if doc.Dependencies != nil {
		assert.Nil(t, pick(doc.Dependencies), "expected %s to be absent", label)
	}
}

// ---- TestBuildDocument_StatusBoardRedaction ----

func TestBuildDocument_StatusBoardRedaction(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-draft", State: "draft", Risk: "high", Blocker: "internal risk detail", UpdatedAt: "2026-05-03"},
			{JourneyID: "J-planned", State: "planned", Risk: "medium", Blocker: "some blocker note", UpdatedAt: "2026-05-03"},
			{JourneyID: "J-doing", State: "doing", Risk: "low", Blocker: "no blocker", UpdatedAt: "2026-05-03"},
			{JourneyID: "J-blocked", State: "blocked", Risk: "high", Blocker: "db migration pending", UpdatedAt: "2026-05-03"},
		},
	}
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Include: catalog.IncludeOptions{StatusBoard: true}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.Len(t, doc.StatusBoard, 4)

	byID := make(map[string]catalog.StatusBoardEntry, 4)
	for _, e := range doc.StatusBoard {
		byID[e.JourneyID] = e
	}

	draft := byID["J-draft"]
	assert.Equal(t, "draft", draft.State)
	assert.Equal(t, "J-draft", draft.JourneyID)
	assert.Equal(t, "2026-05-03", draft.UpdatedAt)
	assert.Empty(t, draft.Risk, "draft entry: risk must be redacted")
	assert.Empty(t, draft.Blocker, "draft entry: blocker must be redacted")

	planned := byID["J-planned"]
	assert.Equal(t, "planned", planned.State)
	assert.Equal(t, "J-planned", planned.JourneyID)
	assert.Equal(t, "2026-05-03", planned.UpdatedAt)
	assert.Empty(t, planned.Risk, "planned entry: risk must be redacted")
	assert.Empty(t, planned.Blocker, "planned entry: blocker must be redacted")

	doing := byID["J-doing"]
	assert.Equal(t, "low", doing.Risk, "doing entry: risk must be preserved")
	assert.Equal(t, "no blocker", doing.Blocker, "doing entry: blocker must be preserved")

	blocked := byID["J-blocked"]
	assert.Equal(t, "high", blocked.Risk, "blocked entry: risk must be preserved")
	assert.Equal(t, "db migration pending", blocked.Blocker, "blocked entry: blocker must be preserved")

	// Verify original pm.StatusBoard is not mutated.
	assert.Equal(t, "high", pm.StatusBoard[0].Risk, "original pm entries must not be mutated")
	assert.Equal(t, "internal risk detail", pm.StatusBoard[0].Blocker, "original pm entries must not be mutated")
}

// ---- TestBuildDocument_PackageDeps_Loading ----

// TestBuildDocument_PackageDeps_Loading verifies that a PackageDepsView with no
// Graph and no Error (loading state: Graph==nil, Error=="") is passed through.
func TestBuildDocument_PackageDeps_Loading(t *testing.T) {
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Filter: catalog.Filter{
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{}, // loading: Graph==nil, Error==""
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Nil(t, doc.Dependencies.Packages.Graph, "loading state: Graph must be nil")
	assert.Empty(t, doc.Dependencies.Packages.Error, "loading state: Error must be empty")
}

// ---- TestBuildDocument_PackageDeps_Error ----

func TestBuildDocument_PackageDeps_Error(t *testing.T) {
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Filter: catalog.Filter{
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{Error: "foo"},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Equal(t, "foo", doc.Dependencies.Packages.Error)
	assert.Nil(t, doc.Dependencies.Packages.Graph)
}

// ---- TestBuildDocument_PackageDeps_Ready ----

func TestBuildDocument_PackageDeps_Ready(t *testing.T) {
	pm := minimalPM()
	g := &kerneldepgraph.Graph{}
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Filter: catalog.Filter{
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{Graph: g},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Same(t, g, doc.Dependencies.Packages.Graph, "Graph pointer identity must be preserved")
}

// ---- TestBuildDocument_QueryEcho ----

func TestBuildDocument_QueryEcho(t *testing.T) {
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Filter: catalog.Filter{
			Kinds:  []string{"Cell"},
			Layers: []string{"cells"},
			Cells:  []string{"accesscore"},
			Include: catalog.IncludeOptions{
				Relations:   true,
				StatusBoard: true,
			},
		},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	assert.Equal(t, []string{"Cell"}, doc.Query.Kinds)
	assert.Equal(t, []string{"cells"}, doc.Query.Layers)
	assert.Equal(t, []string{"accesscore"}, doc.Query.Cells)

	include := doc.Query.Include
	assert.True(t, sort.StringsAreSorted(include), "Include echo must be sorted")
	assert.Contains(t, include, "relations")
	assert.Contains(t, include, "statusBoard")
	assert.NotContains(t, include, "cellDeps")
	assert.NotContains(t, include, "packageDeps")
}

// ---- TestBuildDocument_RejectsNilClock ----

func TestBuildDocument_RejectsNilClock(t *testing.T) {
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock:  nil, // nil clock must be rejected
		Filter: catalog.Filter{Include: catalog.AllIncluded()},
	}
	_, err := catalog.BuildDocument(pm, opts)
	assert.Error(t, err, "nil opts.Clock must return an error")
}

// ---- TestRelationsDeterministic ----

func TestRelationsDeterministic(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()

	first, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		doc, err := catalog.BuildDocument(pm, opts)
		require.NoError(t, err)
		require.Equal(t, len(first.Entities), len(doc.Entities))
		for j := range first.Entities {
			assert.Equal(t, first.Entities[j].Relations, doc.Entities[j].Relations,
				"relations must be deterministic at index %d iteration %d", j, i)
		}
	}
}

// ---- TestBuildDocument_FilterCellsFocus_L0Dep ----

func TestBuildDocument_FilterCellsFocus_L0Dep(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cella": {
				ID:               "cella",
				ConsistencyLevel: "L1",
				L0Dependencies:   []metadata.L0DepMeta{{Cell: "cellb", Reason: "crypto"}},
			},
			"cellb": {
				ID:               "cellb",
				ConsistencyLevel: "L0",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Cells: []string{"cella"}, Include: catalog.IncludeOptions{}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	names := make([]string, 0, len(doc.Entities))
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}
	assert.Contains(t, names, "Cell/cella", "focus cell must be present")
	assert.Contains(t, names, "Cell/cellb", "L0 dep of focus cell must be present (F-A3)")
}

// TestBuildDocument_FilterCellsFocus_ContractClient verifies that when focus=[A]
// and A owns a contract C consumed by cell D, then D appears in filtered entities.
func TestBuildDocument_FilterCellsFocus_ContractClient(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cella": {ID: "cella", ConsistencyLevel: "L1"},
			"celld": {ID: "celld", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.cella.api.v1": {
				ID:        "http.cella.api.v1",
				Kind:      "http",
				OwnerCell: "cella",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cella",
					Clients: []string{"celld"},
				},
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Cells: []string{"cella"}, Include: catalog.IncludeOptions{}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	names := make([]string, 0, len(doc.Entities))
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}
	assert.Contains(t, names, "Cell/cella", "focus cell must be present")
	assert.Contains(t, names, "Cell/celld", "contract client cell must be present (F-A3)")
}

// ---- TestBuildDocument_DepsFilter_CellFocus ----

func TestBuildDocument_DepsFilter_CellFocus(t *testing.T) {
	pm := fullPM()
	cellDeps := &catalog.CellDepGraph{
		Nodes: []string{"accesscore", "auditcore"},
		Edges: []catalog.CellEdge{{From: "accesscore", To: "auditcore"}},
	}
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Root:  "/projects/gocell",
		Filter: catalog.Filter{
			Cells:   []string{"accesscore"},
			Include: catalog.IncludeOptions{CellDeps: true},
		},
		CellDeps: cellDeps,
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Cells)

	for _, n := range doc.Dependencies.Cells.Nodes {
		assert.NotEqual(t, "auditcore", n,
			"auditcore must not appear in filtered cellDeps.Nodes when focus=[accesscore]")
	}
	for _, e := range doc.Dependencies.Cells.Edges {
		assert.False(t, e.From == "accesscore" && e.To == "auditcore",
			"edge accesscore→auditcore must not appear when auditcore is filtered")
	}
}

// TestBuildDocument_DepsFilter_Layers verifies that when filter.Layers=[cells],
// dependencies.packages only contains packages with layer="cells".
func TestBuildDocument_DepsFilter_Layers(t *testing.T) {
	cellsNode := &kerneldepgraph.Node{
		ID:      "github.com/foo/bar/cells/myapp",
		Layer:   "cells",
		Imports: []string{},
	}
	kernelNode := &kerneldepgraph.Node{
		ID:      "github.com/foo/bar/kernel/core",
		Layer:   "kernel",
		Imports: []string{},
	}
	g := kerneldepgraph.FromNodes("github.com/foo/bar", []*kerneldepgraph.Node{cellsNode, kernelNode})
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Root:  "/projects/gocell",
		Filter: catalog.Filter{
			Layers:  []string{"cells"},
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{Graph: g},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	require.NotNil(t, doc.Dependencies.Packages.Graph)

	for _, pkg := range doc.Dependencies.Packages.Graph.Packages {
		assert.Equal(t, "cells", pkg.Layer,
			"filtered packageDeps graph must only contain packages with layer=cells")
	}
}

// TestBuildDocument_DepsFilter_PackageCells verifies that when filter.Cells is
// set, dependencies.packages only contains packages owned by the focused cells.
func TestBuildDocument_DepsFilter_PackageCells(t *testing.T) {
	accessNode := &kerneldepgraph.Node{
		ID:      "github.com/foo/bar/cells/accesscore/session",
		Layer:   "cells",
		CellID:  "accesscore",
		Imports: []string{},
	}
	auditNode := &kerneldepgraph.Node{
		ID:      "github.com/foo/bar/cells/auditcore/audit",
		Layer:   "cells",
		CellID:  "auditcore",
		Imports: []string{},
	}
	kernelNode := &kerneldepgraph.Node{
		ID:      "github.com/foo/bar/kernel/core",
		Layer:   "kernel",
		Imports: []string{},
	}
	g := kerneldepgraph.FromNodes("github.com/foo/bar", []*kerneldepgraph.Node{accessNode, auditNode, kernelNode})
	pm := fullPM()
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Root:  "/projects/gocell",
		Filter: catalog.Filter{
			Cells:   []string{"accesscore"},
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{Graph: g},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	require.NotNil(t, doc.Dependencies.Packages.Graph)

	var ids []string
	for _, pkg := range doc.Dependencies.Packages.Graph.Packages {
		ids = append(ids, pkg.ID)
	}
	assert.Equal(t, []string{"github.com/foo/bar/cells/accesscore/session"}, ids)
}

// ---- NEW TESTS (A5 specification) ----

// TestBuildDocument_Redaction_AllStates verifies risk/blocker redaction for all
// status board states: draft/planned are redacted; doing/blocked/ready are not.
func TestBuildDocument_Redaction_AllStates(t *testing.T) {
	cases := []struct {
		state      string
		risk       string
		blocker    string
		wantRedact bool
	}{
		{state: "draft", risk: "high", blocker: "blocker text", wantRedact: true},
		{state: "planned", risk: "medium", blocker: "another blocker", wantRedact: true},
		{state: "doing", risk: "low", blocker: "doing blocker", wantRedact: false},
		{state: "blocked", risk: "high", blocker: "blocked note", wantRedact: false},
		{state: "ready", risk: "none", blocker: "ready note", wantRedact: false},
	}

	for _, tc := range cases {
		t.Run("state="+tc.state, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:      map[string]*metadata.CellMeta{},
				Slices:     map[string]*metadata.SliceMeta{},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
				Actors:     []metadata.ActorMeta{},
				StatusBoard: []metadata.StatusBoardEntry{
					{
						JourneyID: "J-test",
						State:     tc.state,
						Risk:      tc.risk,
						Blocker:   tc.blocker,
						UpdatedAt: "2026-05-04",
					},
				},
			}
			opts := catalog.ExportOptions{
				Clock:  fixedClock(),
				Filter: catalog.Filter{Include: catalog.IncludeOptions{StatusBoard: true}},
			}
			doc, err := catalog.BuildDocument(pm, opts)
			require.NoError(t, err)
			require.Len(t, doc.StatusBoard, 1)

			entry := doc.StatusBoard[0]
			assert.Equal(t, tc.state, entry.State, "state must be preserved")
			if tc.wantRedact {
				assert.Empty(t, entry.Risk, "risk must be redacted for state=%s", tc.state)
				assert.Empty(t, entry.Blocker, "blocker must be redacted for state=%s", tc.state)
			} else {
				assert.Equal(t, tc.risk, entry.Risk, "risk must be preserved for state=%s", tc.state)
				assert.Equal(t, tc.blocker, entry.Blocker, "blocker must be preserved for state=%s", tc.state)
			}
		})
	}
}

// TestBuildDocument_Filter_MultiDim verifies multi-dimensional filter combinations.
func TestBuildDocument_Filter_MultiDim(t *testing.T) {
	pm := fullPM()

	// kinds×cells: only Cell entities for accesscore
	t.Run("kinds=Cell,cells=accesscore", func(t *testing.T) {
		opts := catalog.ExportOptions{
			Clock: fixedClock(),
			Filter: catalog.Filter{
				Kinds:   []string{"Cell"},
				Cells:   []string{"accesscore"},
				Include: catalog.IncludeOptions{},
			},
		}
		doc, err := catalog.BuildDocument(pm, opts)
		require.NoError(t, err)
		for _, e := range doc.Entities {
			assert.Equal(t, "Cell", e.Kind, "only Cell entities should appear")
		}
		assert.Len(t, doc.Entities, 1, "only accesscore cell entity expected")
	})

	// kinds×layers: kinds=Cell,Slice and layers=cells must give same result as layers=cells alone
	t.Run("kinds=Cell+Slice,layers=cells", func(t *testing.T) {
		opts := catalog.ExportOptions{
			Clock: fixedClock(),
			Filter: catalog.Filter{
				Kinds:   []string{"Cell", "Slice"},
				Layers:  []string{"cells"},
				Include: catalog.IncludeOptions{},
			},
		}
		doc, err := catalog.BuildDocument(pm, opts)
		require.NoError(t, err)
		for _, e := range doc.Entities {
			assert.Contains(t, []string{"Cell", "Slice"}, e.Kind)
		}
	})

	// Three-dimensional: kinds + layers + cells
	t.Run("kinds=Slice,layers=cells,cells=accesscore", func(t *testing.T) {
		opts := catalog.ExportOptions{
			Clock: fixedClock(),
			Filter: catalog.Filter{
				Kinds:   []string{"Slice"},
				Layers:  []string{"cells"},
				Cells:   []string{"accesscore"},
				Include: catalog.IncludeOptions{},
			},
		}
		doc, err := catalog.BuildDocument(pm, opts)
		require.NoError(t, err)
		for _, e := range doc.Entities {
			assert.Equal(t, "Slice", e.Kind, "only Slice entities expected")
			spec, ok := e.Spec.(catalog.SliceSpec)
			require.True(t, ok)
			assert.Equal(t, "accesscore", spec.BelongsToCell, "slice must belong to accesscore")
		}
	})
}

// TestBuildDocument_GeneratedAt_HonorsInjectedClock verifies that the clock
// injection is respected: GeneratedAt == injected clock time.
func TestBuildDocument_GeneratedAt_HonorsInjectedClock(t *testing.T) {
	injected := time.Date(2024, 3, 15, 9, 30, 0, 0, time.UTC)
	clk := clockmock.New(injected)
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock:  clk,
		Filter: catalog.Filter{Include: catalog.IncludeOptions{}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	assert.Equal(t, injected.UTC().Format(time.RFC3339), doc.GeneratedAt,
		"GeneratedAt must equal injected clock time")
}

// ---- TestInjectWireSummaries ----

// TestInjectWireSummaries_CellSpecWire verifies that InjectWireSummaries
// replaces Cell entity Spec with a CellSpecWire embedding the original CellSpec
// plus the matching CellWireSummary. Non-Cell entities are left unchanged.
func TestInjectWireSummaries_CellSpecWire(t *testing.T) {
	entities := []catalog.Entity{
		{
			Kind:     "Cell",
			Metadata: catalog.EntityMetadata{Name: "accesscore"},
			Spec: catalog.CellSpec{
				Type:             "core",
				ConsistencyLevel: "L1",
			},
		},
		{
			Kind:     "Cell",
			Metadata: catalog.EntityMetadata{Name: "auditcore"},
			Spec: catalog.CellSpec{
				Type:             "support",
				ConsistencyLevel: "L2",
			},
		},
		{
			Kind:     "Slice",
			Metadata: catalog.EntityMetadata{Name: "sessionlogin"},
			Spec:     catalog.SliceSpec{BelongsToCell: "accesscore"},
		},
	}

	summaries := []metadata.CellWireSummary{
		{
			CellID: "accesscore",
			Listeners: []metadata.WireListenerView{
				{Ref: "cell.PrimaryListener", Prefix: "/api/v1/access"},
			},
		},
	}

	catalog.InjectWireSummaries(entities, summaries)

	// accesscore must have CellSpecWire with populated WireSummary.
	spec0, ok0 := entities[0].Spec.(catalog.CellSpecWire)
	require.True(t, ok0, "accesscore spec must be CellSpecWire after injection")
	assert.Equal(t, "core", spec0.Type)
	require.NotNil(t, spec0.WireSummary)
	assert.Equal(t, "accesscore", spec0.WireSummary.CellID)
	require.Len(t, spec0.WireSummary.Listeners, 1)
	assert.Equal(t, "/api/v1/access", spec0.WireSummary.Listeners[0].Prefix)

	// auditcore has no matching summary: WireSummary == nil (omitempty in JSON).
	spec1, ok1 := entities[1].Spec.(catalog.CellSpecWire)
	require.True(t, ok1, "auditcore spec must be CellSpecWire after injection")
	assert.Nil(t, spec1.WireSummary, "auditcore WireSummary must be nil when no match")

	// Slice entity must be unchanged.
	_, isSliceSpec := entities[2].Spec.(catalog.SliceSpec)
	assert.True(t, isSliceSpec, "Slice entity must retain its SliceSpec")
}

// TestInjectWireSummaries_EmptySummaries verifies that passing nil summaries
// produces CellSpecWire with nil WireSummary (omitempty). The call-site guard
// `len(opts.WireSummaries) > 0` in BuildDocument prevents this path in
// practice; InjectWireSummaries itself does not skip on nil — it converts to
// CellSpecWire with WireSummary==nil so JSON/YAML output omits the field.
func TestInjectWireSummaries_EmptySummaries(t *testing.T) {
	entities := []catalog.Entity{
		{
			Kind:     "Cell",
			Metadata: catalog.EntityMetadata{Name: "accesscore"},
			Spec:     catalog.CellSpec{Type: "core"},
		},
	}
	catalog.InjectWireSummaries(entities, nil)

	wire, isCellSpecWire := entities[0].Spec.(catalog.CellSpecWire)
	require.True(t, isCellSpecWire, "entity Spec becomes CellSpecWire even with nil summaries")
	assert.Nil(t, wire.WireSummary, "WireSummary must be nil when no matching summary exists")
}

// TestBuildDocument_WireSummariesInjected verifies that BuildDocument populates
// CellSpecWire on Cell entities when opts.WireSummaries is non-empty.
func TestBuildDocument_WireSummariesInjected(t *testing.T) {
	pm := minimalPM() // 1 cell: accesscore
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Include: catalog.IncludeOptions{}},
		WireSummaries: []metadata.CellWireSummary{
			{
				CellID: "accesscore",
				Listeners: []metadata.WireListenerView{
					{Ref: "cell.PrimaryListener", Prefix: "/api/v1/access"},
				},
			},
		},
	}

	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	var cellEntity *catalog.Entity
	for i := range doc.Entities {
		if doc.Entities[i].Kind == "Cell" && doc.Entities[i].Metadata.Name == "accesscore" {
			cellEntity = &doc.Entities[i]
			break
		}
	}
	require.NotNil(t, cellEntity, "accesscore entity must be present")

	wire, ok := cellEntity.Spec.(catalog.CellSpecWire)
	require.True(t, ok, "accesscore spec must be CellSpecWire when WireSummaries are provided")
	require.NotNil(t, wire.WireSummary)
	assert.Equal(t, "accesscore", wire.WireSummary.CellID)
}

// TestBuildWireSummaryIndex_UniqueIDs verifies that buildWireSummaryIndex
// (called via InjectWireSummaries) handles duplicate CellIDs by keeping last.
func TestInjectWireSummaries_DuplicateCellID(t *testing.T) {
	entities := []catalog.Entity{
		{
			Kind:     "Cell",
			Metadata: catalog.EntityMetadata{Name: "accesscore"},
			Spec:     catalog.CellSpec{Type: "core"},
		},
	}
	summaries := []metadata.CellWireSummary{
		{CellID: "accesscore", Listeners: []metadata.WireListenerView{{Ref: "first", Prefix: "/first"}}},
		{CellID: "accesscore", Listeners: []metadata.WireListenerView{{Ref: "second", Prefix: "/second"}}},
	}

	catalog.InjectWireSummaries(entities, summaries)

	wire, ok := entities[0].Spec.(catalog.CellSpecWire)
	require.True(t, ok)
	require.NotNil(t, wire.WireSummary)
	// buildWireSummaryIndex iterates and last write wins.
	assert.Equal(t, "/second", wire.WireSummary.Listeners[0].Prefix,
		"last summary with same CellID should win")
}

// ---- TestBuildDocument_PackageDeps_NoStatusField ----

// TestBuildDocument_PackageDeps_NoStatusField verifies that PackageDepsView
// wire output does not contain a "status" key in the JSON output (A4 compliance).
func TestBuildDocument_PackageDeps_NoStatusField(t *testing.T) {
	pm := minimalPM()
	g := &kerneldepgraph.Graph{}
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Filter: catalog.Filter{
			Include: catalog.IncludeOptions{PackageDeps: true},
		},
		Packages: &catalog.PackageDepsView{Graph: g},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	// Marshal to JSON and verify no "status" key appears in packages block.
	body, err := catalog.MarshalDocument(doc, "json")
	require.NoError(t, err)

	// The JSON must not contain a "status" key at all in this output.
	bodyStr := string(body)
	assert.NotContains(t, bodyStr, `"status"`,
		"PackageDepsView wire output must not contain 'status' key (A4 compliance)")
}
