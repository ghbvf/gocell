// Package metadata_test · export_test.go: TDD tests for BuildDocument + MarshalDocument.
// Uses package metadata_test (external test package) to avoid import cycles.
package metadata_test

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
)

var update = flag.Bool("update", false, "update golden files")

// fixedNow is an anchored timestamp for deterministic test output.
var fixedNow = time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

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

// opts returns a baseline ExportOptions.
func baseOpts() metadata.ExportOptions {
	return metadata.ExportOptions{
		Now:    fixedNow,
		Root:   "/projects/gocell",
		Filter: metadata.Filter{Include: metadata.IncludeAll},
	}
}

// ---- TestBuildDocument_FullSnapshot ----

func TestBuildDocument_FullSnapshot(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	assert.Equal(t, "v1", doc.SchemaVersion)
	assert.Equal(t, "gocell.io/v1alpha1", doc.APIVersion)
	assert.Equal(t, fixedNow.UTC().Format(time.RFC3339), doc.GeneratedAt)
	assert.Equal(t, "/projects/gocell", doc.Root)

	// With IncludeAll, statusBoard should be present.
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
			_, ok := e.Spec.(metadata.CellSpec)
			assert.True(t, ok, "Cell entity should have CellSpec, got %T", e.Spec)
		case "Slice":
			_, ok := e.Spec.(metadata.SliceSpec)
			assert.True(t, ok, "Slice entity should have SliceSpec, got %T", e.Spec)
		case "Contract":
			_, ok := e.Spec.(metadata.ContractSpec)
			assert.True(t, ok, "Contract entity should have ContractSpec, got %T", e.Spec)
		case "Journey":
			_, ok := e.Spec.(metadata.JourneySpec)
			assert.True(t, ok, "Journey entity should have JourneySpec, got %T", e.Spec)
		case "Assembly":
			_, ok := e.Spec.(metadata.AssemblySpec)
			assert.True(t, ok, "Assembly entity should have AssemblySpec, got %T", e.Spec)
		case "Actor":
			_, ok := e.Spec.(metadata.ActorSpec)
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
	doc, err := metadata.BuildDocument(pm, opts)
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
	doc, err := metadata.BuildDocument(pm, opts)
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
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	// accesscore cell + its slices + contracts it participates in + contract owners
	// At minimum: accesscore cell, sessionlogin slice, http.access.login.v1 contract
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
	// Build a ProjectMeta where:
	//   - accesscore does NOT own event.audit.entry.v1 (owned by auditcore)
	//   - but accesscore.sessionlogin subscribes to it via contractUsages
	pm := fullPM()
	// Add a contractUsage on the existing sessionlogin slice to subscribe to
	// event.audit.entry.v1 (which is owned by auditcore, not accesscore).
	pm.Slices["accesscore/sessionlogin"].ContractUsages = append(
		pm.Slices["accesscore/sessionlogin"].ContractUsages,
		metadata.ContractUsage{Contract: "event.audit.entry.v1", Role: "subscribe"},
	)

	opts := baseOpts()
	opts.Filter.Cells = []string{"accesscore"}
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	names := make([]string, 0, len(doc.Entities))
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}

	// accesscore is the focus cell — must be present.
	assert.Contains(t, names, "Cell/accesscore")
	// sessionlogin belongs to accesscore — must be present.
	assert.Contains(t, names, "Slice/sessionlogin")
	// http.access.login.v1 is owned by accesscore — must be present.
	assert.Contains(t, names, "Contract/http.access.login.v1")
	// event.audit.entry.v1 is owned by auditcore but consumed by accesscore —
	// consumer-side match must include it.
	assert.Contains(t, names, "Contract/event.audit.entry.v1",
		"contract consumed by focus cell must appear even when owned by another cell")

	// auditcore cell itself should NOT appear (it's not a direct neighbor in cell-graph terms).
	for _, n := range names {
		assert.NotEqual(t, "Cell/auditcore", n,
			"auditcore cell itself should not appear when focusing on accesscore")
	}
}

// ---- TestBuildDocument_IncludeMask ----

func TestBuildDocument_IncludeMask(t *testing.T) {
	cellDeps := &metadata.CellDepGraph{
		Nodes: []string{"accesscore"},
		Edges: []metadata.CellEdge{},
	}
	pkgs := &metadata.PackageDepsView{
		Status: "ready",
		Graph:  &kerneldepgraph.Graph{},
	}
	pm := fullPM()

	cases := []struct {
		name          string
		mask          metadata.IncludeMask
		wantRelations bool // at least one entity has relations
		wantStatus    bool // statusBoard present
		wantCellDeps  bool // dependencies.cells present
		wantPkgDeps   bool // dependencies.packages present
	}{
		{
			name:          "IncludeRelations only",
			mask:          metadata.IncludeRelations,
			wantRelations: true,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "IncludeStatusBoard only",
			mask:          metadata.IncludeStatusBoard,
			wantRelations: false,
			wantStatus:    true,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "IncludeCellDeps only",
			mask:          metadata.IncludeCellDeps,
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  true,
			wantPkgDeps:   false,
		},
		{
			name:          "IncludePackageDeps only",
			mask:          metadata.IncludePackageDeps,
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   true,
		},
		{
			name:          "IncludeAll",
			mask:          metadata.IncludeAll,
			wantRelations: true,
			wantStatus:    true,
			wantCellDeps:  true,
			wantPkgDeps:   true,
		},
		{
			name:          "zero mask",
			mask:          0,
			wantRelations: false,
			wantStatus:    false,
			wantCellDeps:  false,
			wantPkgDeps:   false,
		},
		{
			name:          "Relations+CellDeps",
			mask:          metadata.IncludeRelations | metadata.IncludeCellDeps,
			wantRelations: true,
			wantStatus:    false,
			wantCellDeps:  true,
			wantPkgDeps:   false,
		},
		{
			name:          "StatusBoard+PackageDeps",
			mask:          metadata.IncludeStatusBoard | metadata.IncludePackageDeps,
			wantRelations: false,
			wantStatus:    true,
			wantCellDeps:  false,
			wantPkgDeps:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := metadata.ExportOptions{
				Now:      fixedNow,
				Filter:   metadata.Filter{Include: tc.mask},
				CellDeps: cellDeps,
				Packages: pkgs,
			}
			doc, err := metadata.BuildDocument(pm, opts)
			require.NoError(t, err)

			hasRelations := false
			for _, e := range doc.Entities {
				if len(e.Relations) > 0 {
					hasRelations = true
					break
				}
			}
			if tc.wantRelations {
				assert.True(t, hasRelations, "expected relations to be present")
			} else {
				assert.False(t, hasRelations, "expected relations to be absent")
			}

			if tc.wantStatus {
				assert.NotEmpty(t, doc.StatusBoard, "expected statusBoard to be present")
			} else {
				assert.Empty(t, doc.StatusBoard, "expected statusBoard to be absent")
			}

			if tc.wantCellDeps {
				require.NotNil(t, doc.Dependencies, "expected Dependencies block")
				assert.NotNil(t, doc.Dependencies.Cells, "expected Dependencies.Cells")
			} else if doc.Dependencies != nil {
				assert.Nil(t, doc.Dependencies.Cells, "expected Dependencies.Cells to be absent")
			}

			if tc.wantPkgDeps {
				require.NotNil(t, doc.Dependencies, "expected Dependencies block")
				assert.NotNil(t, doc.Dependencies.Packages, "expected Dependencies.Packages")
			} else if doc.Dependencies != nil {
				assert.Nil(t, doc.Dependencies.Packages, "expected Dependencies.Packages to be absent")
			}
		})
	}
}

// ---- TestBuildDocument_PackageDepsLoading ----

func TestBuildDocument_PackageDepsLoading(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now: fixedNow,
		Filter: metadata.Filter{
			Include: metadata.IncludePackageDeps,
		},
		Packages: &metadata.PackageDepsView{Status: "loading"},
	}
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Equal(t, "loading", doc.Dependencies.Packages.Status)
	assert.Nil(t, doc.Dependencies.Packages.Graph)
}

// ---- TestBuildDocument_PackageDepsError ----

func TestBuildDocument_PackageDepsError(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now: fixedNow,
		Filter: metadata.Filter{
			Include: metadata.IncludePackageDeps,
		},
		Packages: &metadata.PackageDepsView{Status: "error", Error: "foo"},
	}
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Equal(t, "error", doc.Dependencies.Packages.Status)
	assert.Equal(t, "foo", doc.Dependencies.Packages.Error)
}

// ---- TestBuildDocument_PackageDepsReady ----

func TestBuildDocument_PackageDepsReady(t *testing.T) {
	pm := minimalPM()
	g := &kerneldepgraph.Graph{}
	opts := metadata.ExportOptions{
		Now: fixedNow,
		Filter: metadata.Filter{
			Include: metadata.IncludePackageDeps,
		},
		Packages: &metadata.PackageDepsView{Status: "ready", Graph: g},
	}
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.NotNil(t, doc.Dependencies)
	require.NotNil(t, doc.Dependencies.Packages)
	assert.Same(t, g, doc.Dependencies.Packages.Graph, "Graph pointer identity must be preserved")
}

// ---- TestBuildDocument_QueryEcho ----

func TestBuildDocument_QueryEcho(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now: fixedNow,
		Filter: metadata.Filter{
			Kinds:   []string{"Cell"},
			Layers:  []string{"cells"},
			Cells:   []string{"accesscore"},
			Include: metadata.IncludeRelations | metadata.IncludeStatusBoard,
		},
	}
	doc, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	assert.Equal(t, []string{"Cell"}, doc.Query.Kinds)
	assert.Equal(t, []string{"cells"}, doc.Query.Layers)
	assert.Equal(t, []string{"accesscore"}, doc.Query.Cells)

	// Include echoed as sorted []string
	include := doc.Query.Include
	assert.True(t, sort.StringsAreSorted(include), "Include echo must be sorted")
	assert.Contains(t, include, "relations")
	assert.Contains(t, include, "statusBoard")
	assert.NotContains(t, include, "cellDeps")
	assert.NotContains(t, include, "packageDeps")
}

// ---- TestBuildDocument_RejectsZeroNow ----

func TestBuildDocument_RejectsZeroNow(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now:    time.Time{}, // zero
		Filter: metadata.Filter{Include: metadata.IncludeAll},
	}
	_, err := metadata.BuildDocument(pm, opts)
	assert.Error(t, err, "zero opts.Now must return an error")
}

// ---- TestRelationsDeterministic ----

func TestRelationsDeterministic(t *testing.T) {
	pm := fullPM()
	opts := baseOpts()

	first, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		doc, err := metadata.BuildDocument(pm, opts)
		require.NoError(t, err)
		require.Equal(t, len(first.Entities), len(doc.Entities))
		for j := range first.Entities {
			assert.Equal(t, first.Entities[j].Relations, doc.Entities[j].Relations,
				"relations must be deterministic at index %d iteration %d", j, i)
		}
	}
}

// ---- TestSchemaVersionFrozen / TestAPIVersionFrozen ----

func TestSchemaVersionFrozen(t *testing.T) {
	assert.Equal(t, "v1", metadata.SchemaVersionV1)
}

func TestAPIVersionFrozen(t *testing.T) {
	assert.Equal(t, "gocell.io/v1alpha1", metadata.APIVersionV1)
}

// ---- TestMarshalDocument_JSONGolden ----

func TestMarshalDocument_JSONGolden(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now:    fixedNow,
		Root:   "/projects/gocell",
		Filter: metadata.Filter{Include: metadata.IncludeAll},
	}
	d, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := metadata.MarshalDocument(d, "json")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_minimal.json"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_YAMLGolden ----

func TestMarshalDocument_YAMLGolden(t *testing.T) {
	pm := minimalPM()
	opts := metadata.ExportOptions{
		Now:    fixedNow,
		Root:   "/projects/gocell",
		Filter: metadata.Filter{Include: metadata.IncludeAll},
	}
	d, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := metadata.MarshalDocument(d, "yaml")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_full.yaml"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_FilteredGolden ----

func TestMarshalDocument_FilteredGolden(t *testing.T) {
	pm := fullPM()
	cellDeps := &metadata.CellDepGraph{
		Nodes: []string{"accesscore", "auditcore"},
		Edges: []metadata.CellEdge{{From: "accesscore", To: "auditcore"}},
	}
	opts := metadata.ExportOptions{
		Now:  fixedNow,
		Root: "/projects/gocell",
		Filter: metadata.Filter{
			Cells:   []string{"accesscore"},
			Include: metadata.IncludeCellDeps | metadata.IncludePackageDeps,
		},
		CellDeps: cellDeps,
		Packages: &metadata.PackageDepsView{Status: "loading"},
	}
	d, err := metadata.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := metadata.MarshalDocument(d, "json")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_filtered.json"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_BadFormat ----

func TestMarshalDocument_BadFormat(t *testing.T) {
	d := metadata.Document{}
	_, err := metadata.MarshalDocument(d, "xml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "xml")
}

// ---- TestMarshalDocument_EmptyDocument ----

func TestMarshalDocument_EmptyDocument(t *testing.T) {
	d := metadata.Document{}
	got, err := metadata.MarshalDocument(d, "json")
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(got, &m))
	assert.Contains(t, m, "schemaVersion", "schemaVersion field must be present")
	assert.Contains(t, m, "apiVersion", "apiVersion field must be present")
}

// ---- TestCamelCaseTags ----

func TestCamelCaseTags(t *testing.T) {
	roots := []any{
		metadata.Document{},
		metadata.Entity{},
		metadata.EntityMetadata{},
		metadata.Relation{},
		metadata.Dependencies{},
		metadata.CellDepGraph{},
		metadata.CellEdge{},
		metadata.PackageDepsView{},
		metadata.FilterEcho{},
		metadata.CellSpec{},
		metadata.CellSpecOwner{},
		metadata.CellSpecSchema{},
		metadata.CellSpecL0Dep{},
		metadata.SliceSpec{},
		metadata.SliceSpecContractUsage{},
		metadata.ContractSpec{},
		metadata.JourneySpec{},
		metadata.JourneyPassCrit{},
		metadata.AssemblySpec{},
		metadata.AssemblySpecBuild{},
		metadata.ActorSpec{},
	}

	for _, root := range roots {
		typ := reflect.TypeOf(root)
		checkExportTags(t, typ, typ.Name())
	}
}

func checkExportTags(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		fieldPath := path + "." + f.Name
		jsonTag, hasJSON := f.Tag.Lookup("json")
		yamlTag, hasYAML := f.Tag.Lookup("yaml")

		if !hasJSON || !hasYAML {
			t.Errorf("field %s missing json or yaml tag (json=%v, yaml=%v)", fieldPath, hasJSON, hasYAML)
			continue
		}

		jsonName := strings.Split(jsonTag, ",")[0]
		yamlName := strings.Split(yamlTag, ",")[0]

		if jsonName == "-" || yamlName == "-" {
			continue
		}
		if jsonName == "" || yamlName == "" {
			continue
		}

		// json tag must start with lowercase
		if len(jsonName) > 0 && jsonName[0] >= 'A' && jsonName[0] <= 'Z' {
			t.Errorf("field %s json tag %q starts with uppercase", fieldPath, jsonName)
		}
		// json and yaml tag names must match
		if jsonName != yamlName {
			t.Errorf("field %s json tag %q != yaml tag %q", fieldPath, jsonName, yamlName)
		}
	}
}
