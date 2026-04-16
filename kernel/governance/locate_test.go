package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// parseNode is a test helper that parses src into a yaml.Node (DocumentNode).
func parseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &n))
	return &n
}

// TestValidator_Locate_KnownField: locate returns the line/column for an
// existing field in the stored yaml.Node.
func TestValidator_Locate_KnownField(t *testing.T) {
	src := "id: access-core\n" + // line 1
		"type: core\n" + // line 2
		"owner:\n" + // line 3
		"  team: platform\n" // line 4

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Nodes: map[string]*yaml.Node{
			"cells/access-core/cell.yaml": parseNode(t, src),
		},
	}
	v := NewValidator(pm, "")

	line, col := v.locate("cells/access-core/cell.yaml", "id")
	assert.Equal(t, 1, line, "id line")
	assert.Positive(t, col, "id column")

	line, col = v.locate("cells/access-core/cell.yaml", "owner.team")
	assert.Equal(t, 4, line, "owner.team line")
	assert.Positive(t, col, "owner.team column")
}

// TestValidator_Locate_Fallbacks: empty file, empty field, missing Nodes,
// missing file entry, and missing field all return (0, 0).
func TestValidator_Locate_Fallbacks(t *testing.T) {
	pm := &metadata.ProjectMeta{Cells: map[string]*metadata.CellMeta{}}
	v := NewValidator(pm, "")

	// Nodes map entirely absent.
	line, col := v.locate("foo.yaml", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)

	// Nodes map present but empty.
	pm.Nodes = map[string]*yaml.Node{}
	line, col = v.locate("foo.yaml", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)

	// File present but field not found.
	pm.Nodes["foo.yaml"] = parseNode(t, "id: x\n")
	line, col = v.locate("foo.yaml", "nope")
	assert.Zero(t, line)
	assert.Zero(t, col)

	// Empty file / field arguments.
	line, col = v.locate("", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)
	line, col = v.locate("foo.yaml", "")
	assert.Zero(t, line)
	assert.Zero(t, col)
}

// TestValidator_NewResult_AutoFillsLocation: newResult constructs a
// ValidationResult and auto-populates Line/Column from the stored Node.
func TestValidator_NewResult_AutoFillsLocation(t *testing.T) {
	src := "id: access-core\n" + // line 1
		"contractUsages:\n" + // line 2
		"  - contract: http.a.v1\n" + // line 3
		"    role: serve\n" + // line 4
		"  - contract: http.b.v1\n" + // line 5
		"    role: call\n" // line 6

	pm := &metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
		Nodes: map[string]*yaml.Node{
			"cells/x/slices/s/slice.yaml": parseNode(t, src),
		},
	}
	v := NewValidator(pm, "")

	r := v.newResult("REF-02", SeverityError, IssueRefNotFound,
		"cells/x/slices/s/slice.yaml", "contractUsages[1].contract",
		"references non-existent contract")

	assert.Equal(t, "REF-02", r.Code)
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueRefNotFound, r.IssueType)
	assert.Equal(t, "cells/x/slices/s/slice.yaml", r.File)
	assert.Equal(t, "contractUsages[1].contract", r.Field)
	assert.Equal(t, "references non-existent contract", r.Message)
	assert.Equal(t, 5, r.Line, "line should match contractUsages[1].contract")
	assert.Positive(t, r.Column)
}

// TestValidator_NewResult_UnknownLocation: when the path cannot be located,
// the result is still valid but Line/Column remain zero.
func TestValidator_NewResult_UnknownLocation(t *testing.T) {
	pm := &metadata.ProjectMeta{Cells: map[string]*metadata.CellMeta{}}
	v := NewValidator(pm, "")

	r := v.newResult("REF-01", SeverityError, IssueRefNotFound,
		"cells/x/slice.yaml", "belongsToCell",
		"slice references non-existent cell")

	assert.Equal(t, "REF-01", r.Code)
	assert.Zero(t, r.Line)
	assert.Zero(t, r.Column)
}

// TestValidationResult_PositionFields: the struct exposes Line and Column
// for external inspection (CLI and exported JSON).
func TestValidationResult_PositionFields(t *testing.T) {
	r := ValidationResult{
		Code: "X", File: "f.yaml", Field: "id",
		Line: 42, Column: 7,
	}
	assert.Equal(t, 42, r.Line)
	assert.Equal(t, 7, r.Column)
}

// TestDependencyChecker_NewResult_AutoFillsLocation mirrors the Validator
// test to confirm DependencyChecker gets the same location enrichment via
// the embedded locator.
func TestDependencyChecker_NewResult_AutoFillsLocation(t *testing.T) {
	src := "id: s\n" + // line 1
		"belongsToCell: ghost\n" + // line 2 — the field we'll locate
		"contractUsages: []\n" // line 3

	pm := &metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
		Nodes: map[string]*yaml.Node{
			"cells/x/slices/s/slice.yaml": parseNode(t, src),
		},
	}
	dc := NewDependencyChecker(pm)

	r := dc.newResult("DEP-01", SeverityError, IssueMismatch,
		"cells/x/slices/s/slice.yaml", "belongsToCell",
		"slice belongsToCell mismatch")

	assert.Equal(t, 2, r.Line)
	assert.Positive(t, r.Column)
	assert.Equal(t, "DEP-01", r.Code)
}

// TestDependencyChecker_Locate_FallsBack verifies the nil-project / missing
// Nodes fallback shared with Validator.
func TestDependencyChecker_Locate_FallsBack(t *testing.T) {
	dc := NewDependencyChecker(nil)
	// Safe on a nil project (NewDependencyChecker stores it as-is but locate
	// short-circuits on l.project == nil).
	line, col := dc.locate("any.yaml", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)
}
