package governance_test

import (
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocation_REF01_SliceBelongsToCell verifies that a REF-01 finding
// (slice.belongsToCell pointing at a non-existent cell) carries the exact
// line/column of the offending belongsToCell scalar. This is the headline
// end-to-end assertion: ParseFS → Validator.Validate → IDE-jumpable
// Line/Column on the right field, not a zero placeholder.
//
// The slice directory name ("ghost") matches belongsToCell so that the
// parser's G-7 check does not reject the file before Validator runs; the
// cell itself is simply absent from ProjectMeta.
func TestLocation_REF01_SliceBelongsToCell(t *testing.T) {
	fs := fstest.MapFS{
		// Note: no cells/ghost/cell.yaml — REF-01 fires because the cell does
		// not exist.
		"cells/ghost/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(
			"id: s\n" + // line 1
				"belongsToCell: ghost\n" + // line 2 — target of REF-01
				"contractUsages: []\n" + // line 3
				"verify: {unit: [], contract: []}\n" + // line 4
				"allowedFiles:\n" + // line 5
				"  - cells/ghost/slices/s/**\n", // line 6
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	v := governance.NewValidator(pm, "")
	results := v.Validate()

	var ref01 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "REF-01" && r.File == "cells/ghost/slices/s/slice.yaml" {
			ref01 = r
			break
		}
	}
	require.NotNil(t, ref01, "expected a REF-01 finding")
	assert.Equal(t, governance.SeverityError, ref01.Severity)
	assert.Equal(t, "belongsToCell", ref01.Field)
	assert.Equal(t, 2, ref01.Line, "belongsToCell should be on line 2")
	assert.Positive(t, ref01.Column)
	assert.Contains(t, ref01.Message, "ghost")
}

// TestLocation_REF02_ContractUsageIndex exercises an array-indexed field path
// to confirm that the locator walks contractUsages[i].contract correctly.
func TestLocation_REF02_ContractUsageIndex(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: x\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
		"cells/x/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(
			"id: s\n" + // line 1
				"belongsToCell: x\n" + // line 2
				"contractUsages:\n" + // line 3
				"  - contract: http.ghost.v1\n" + // line 4 — missing ref → REF-02
				"    role: serve\n" + // line 5
				"verify: {unit: [], contract: []}\n" + // line 6
				"allowedFiles:\n  - foo/**\n", // line 7-8
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	v := governance.NewValidator(pm, "")
	results := v.Validate()

	var ref02 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "REF-02" && r.File == "cells/x/slices/s/slice.yaml" {
			ref02 = r
			break
		}
	}
	require.NotNil(t, ref02, "expected a REF-02 finding")
	assert.Equal(t, "contractUsages[0].contract", ref02.Field)
	assert.Equal(t, 4, ref02.Line, "contractUsages[0].contract should be on line 4")
	assert.Positive(t, ref02.Column)
}

// TestLocation_REF14_ConsumerActor locks the fix for the rule-level field
// path drift reported in the follow-up review: REF-14 used to encode the
// consumer field as the logical "consumers" name, which does not exist in
// the YAML file (real kinds are clients/subscribers/invokers/readers).
// Without this fix Line/Column silently degraded to 0.
func TestLocation_REF14_ConsumerActor(t *testing.T) {
	fs := fstest.MapFS{
		"cells/svc/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: svc\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
		"contracts/http/ping/v1/contract.yaml": &fstest.MapFile{Data: []byte(
			"id: http.ping.v1\n" + // line 1
				"kind: http\n" + // line 2
				"lifecycle: active\n" + // line 3
				"endpoints:\n" + // line 4
				"  server: svc\n" + // line 5
				"  clients:\n" + // line 6
				"    - unknown-actor\n", // line 7 — REF-14 target
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	v := governance.NewValidator(pm, "")
	results := v.Validate()

	var ref14 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "REF-14" {
			ref14 = r
			break
		}
	}
	require.NotNil(t, ref14, "expected a REF-14 finding")
	assert.Equal(t, "contracts/http/ping/v1/contract.yaml", ref14.File)
	assert.Equal(t, "endpoints.clients[0]", ref14.Field,
		"REF-14 field must match the kind-specific YAML key, not the logical 'consumers'")
	assert.Equal(t, 7, ref14.Line, "unknown-actor is on line 7")
	assert.Positive(t, ref14.Column)
}

// TestLocation_ADV04_StatusBoardEntry locks the root-sequence location fix:
// journeys/status-board.yaml's top level is a YAML sequence, so ADV-04's
// field path has to use the "[i].journeyId" form the locator now supports.
func TestLocation_ADV04_StatusBoardEntry(t *testing.T) {
	fs := fstest.MapFS{
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(
			"- journeyId: J-real\n" + // line 1
				"  state: green\n" + // line 2
				"  risk: none\n" + // line 3
				"  blocker: \"\"\n" + // line 4
				"  updatedAt: \"2026-01-01\"\n" + // line 5
				"- journeyId: J-ghost\n" + // line 6 — ADV-04 target
				"  state: green\n" + // line 7
				"  risk: none\n" + // line 8
				"  blocker: \"\"\n" + // line 9
				"  updatedAt: \"2026-01-01\"\n", // line 10
		)},
		"journeys/J-real.yaml": &fstest.MapFile{Data: []byte(
			"id: J-real\n" +
				"goal: x\n" +
				"owner: {team: t, role: r}\n" +
				"cells: []\n" +
				"contracts: []\n" +
				"passCriteria: []\n",
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	v := governance.NewValidator(pm, "")
	results := v.Validate()

	var adv04 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "ADV-04" {
			adv04 = r
			break
		}
	}
	require.NotNil(t, adv04, "expected an ADV-04 finding")
	assert.Equal(t, "journeys/status-board.yaml", adv04.File)
	assert.Equal(t, "[1].journeyId", adv04.Field)
	assert.Equal(t, 6, adv04.Line, "second entry's journeyId is on line 6")
	assert.Positive(t, adv04.Column)
}

// TestLocation_DEP02_ScopeNotFile verifies DEP-02 (cycle across cells) uses
// Scope instead of a fake file path so CLI output does not mislead users
// into clicking on "project" as if it were a path.
func TestLocation_DEP02_ScopeNotFile(t *testing.T) {
	// Build two cells that depend on each other through contracts to force
	// a cycle: a's slice serves contract, b's slice calls it and serves
	// another that a's slice calls. Since DEP-02 wiring is complex, we
	// construct the minimum ProjectMeta that makes the dependency graph
	// itself circular.
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"a": {ID: "a", ConsistencyLevel: "L1"},
			"b": {ID: "b", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"a/as": {ID: "as", BelongsToCell: "a", ContractUsages: []metadata.ContractUsage{
				{Contract: "http.ab.v1", Role: "serve"},
				{Contract: "http.ba.v1", Role: "call"},
			}},
			"b/bs": {ID: "bs", BelongsToCell: "b", ContractUsages: []metadata.ContractUsage{
				{Contract: "http.ba.v1", Role: "serve"},
				{Contract: "http.ab.v1", Role: "call"},
			}},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.ab.v1": {ID: "http.ab.v1", Kind: "http", OwnerCell: "a",
				Endpoints: metadata.EndpointsMeta{Server: "a", Clients: []string{"b"}}},
			"http.ba.v1": {ID: "http.ba.v1", Kind: "http", OwnerCell: "b",
				Endpoints: metadata.EndpointsMeta{Server: "b", Clients: []string{"a"}}},
		},
	}

	dc := governance.NewDependencyChecker(pm)
	results := dc.Check()

	var dep02 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "DEP-02" {
			dep02 = r
			break
		}
	}
	require.NotNil(t, dep02, "expected DEP-02 cycle finding")
	assert.Equal(t, "project", dep02.Scope, "DEP-02 must use Scope, not File")
	assert.Empty(t, dep02.File, "File must be empty when Scope is set")
	assert.Zero(t, dep02.Line, "scoped findings never carry Line/Column")
}

// TestLocation_NoNodes_NoCrash confirms that validators tolerate a
// ProjectMeta without FileNodes (e.g. constructed manually in a test): the Line
// and Column fields are simply zero.
func TestLocation_NoNodes_NoCrash(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"x/s": {ID: "s", BelongsToCell: "ghost"},
		},
		Contracts: map[string]*metadata.ContractMeta{},
	}
	v := governance.NewValidator(pm, "")
	results := v.Validate()

	// At least the REF-01 (cell not found) should fire.
	seenRef01 := false
	for _, r := range results {
		if r.Code == "REF-01" {
			seenRef01 = true
			assert.Zero(t, r.Line, "Line should be 0 without FileNodes")
			assert.Zero(t, r.Column, "Column should be 0 without FileNodes")
		}
	}
	assert.True(t, seenRef01, "REF-01 should be produced")
}
