package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestValidator_NewWarning: newWarning sets SeverityWarning and a non-empty Fix
// (the typed-Fix contract is severity-agnostic — warnings carry structured
// remediation too).
func TestValidator_NewWarning(t *testing.T) {
	src := "id: accesscore\n" + // line 1
		"type: core\n" // line 2

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
	}
	prepareNode(t, pm, "cells/accesscore/cell.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	r := v.newWarning(codeREF01, IssueRefNotFound,
		"cells/accesscore/cell.yaml", "id",
		"advisory: cell id unreferenced by any journey",
		"reference the cell from a journey or remove it if unused")

	assert.Equal(t, codeREF01, r.Code)
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.Equal(t, IssueRefNotFound, r.IssueType)
	assert.Equal(t, "cells/accesscore/cell.yaml", r.File)
	assert.Equal(t, "id", r.Field)
	assert.Equal(t, "advisory: cell id unreferenced by any journey", r.Message)
	assert.Equal(t, "reference the cell from a journey or remove it if unused", r.Fix)
	assert.Equal(t, 1, r.Line)
	assert.Positive(t, r.Column)
}

// TestValidator_NewScopedError: newScopedError sets SeverityError, non-empty Scope,
// non-empty Fix, and zero Line/Column (scoped = no single file position).
func TestValidator_NewScopedError(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
	}
	v := NewValidator(pm, "", clock.Real())

	r := v.newScopedError(codeDEP01, IssueMismatch,
		"project", "cells",
		"circular dependency detected: a -> b -> a",
		"remove the dependency cycle by restructuring cell contracts")

	assert.Equal(t, codeDEP01, r.Code)
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueMismatch, r.IssueType)
	assert.NotEmpty(t, r.Scope, "scoped error must have a non-empty Scope")
	assert.Equal(t, "project", r.Scope)
	assert.Equal(t, "cells", r.Field)
	assert.Equal(t, "circular dependency detected: a -> b -> a", r.Message)
	assert.NotEmpty(t, r.Fix, "SeverityError findings must carry Fix guidance")
	assert.Equal(t, "remove the dependency cycle by restructuring cell contracts", r.Fix)
	assert.Zero(t, r.Line, "scoped error has no single file position")
	assert.Zero(t, r.Column, "scoped error has no single file position")
}

// TestNewErrorAt: newErrorAt (package-level func, no receiver) sets SeverityError
// and propagates Line/Column from the supplied metadata.Position.
func TestNewErrorAt(t *testing.T) {
	pos := metadata.Position{Line: 42, Column: 7}

	r := newErrorAt(codeREF01, IssueForbidden,
		"contracts/http/foo/v1/contract.yaml", pos,
		"content",
		"active document contains legacy literal",
		"replace the legacy literal with the approved replacement")

	assert.Equal(t, codeREF01, r.Code)
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueForbidden, r.IssueType)
	assert.Equal(t, "contracts/http/foo/v1/contract.yaml", r.File)
	assert.Equal(t, "content", r.Field)
	assert.Equal(t, "active document contains legacy literal", r.Message)
	assert.NotEmpty(t, r.Fix, "SeverityError findings must carry Fix guidance")
	assert.Equal(t, "replace the legacy literal with the approved replacement", r.Fix)
	assert.Equal(t, 42, r.Line, "Line must come from supplied Position")
	assert.Equal(t, 7, r.Column, "Column must come from supplied Position")
}

// prepareNode is a test helper that stores a YAML source as a file node
// on the ProjectMeta via the public PrepareFileNode method.
func prepareNode(t *testing.T, pm *metadata.ProjectMeta, file, src string) {
	t.Helper()
	require.NoError(t, pm.PrepareFileNode(file, []byte(src)))
}

// TestValidator_Locate_KnownField: locate returns the line/column for an
// existing field in the stored yaml.Node.
func TestValidator_Locate_KnownField(t *testing.T) {
	src := "id: accesscore\n" + // line 1
		"type: core\n" + // line 2
		"owner:\n" + // line 3
		"  team: platform\n" // line 4

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
	}
	prepareNode(t, pm, "cells/accesscore/cell.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	line, col := v.locate("cells/accesscore/cell.yaml", "id")
	assert.Equal(t, 1, line, "id line")
	assert.Positive(t, col, "id column")

	line, col = v.locate("cells/accesscore/cell.yaml", "owner.team")
	assert.Equal(t, 4, line, "owner.team line")
	assert.Positive(t, col, "owner.team column")
}

// TestValidator_Locate_Fallbacks: empty file, empty field, missing file nodes,
// missing file entry, and missing field all return (0, 0).
func TestValidator_Locate_Fallbacks(t *testing.T) {
	pm := &metadata.ProjectMeta{Cells: map[string]*metadata.CellMeta{}}
	v := NewValidator(pm, "", clock.Real())

	// No file nodes set at all.
	line, col := v.locate("foo.yaml", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)

	// File present but field not found.
	prepareNode(t, pm, "foo.yaml", "id: x\n")
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

// TestValidator_NewError_AutoFillsLocation: newError constructs a
// ValidationResult and auto-populates Line/Column from the stored Node.
func TestValidator_NewError_AutoFillsLocation(t *testing.T) {
	src := "id: accesscore\n" + // line 1
		"contractUsages:\n" + // line 2
		"  - contract: http.a.v1\n" + // line 3
		"    role: serve\n" + // line 4
		"  - contract: http.b.v1\n" + // line 5
		"    role: call\n" // line 6

	pm := &metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
	}
	prepareNode(t, pm, "cells/x/slices/s/slice.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	r := v.newError(codeREF02, IssueRefNotFound,
		"cells/x/slices/s/slice.yaml", "contractUsages[1].contract",
		"references non-existent contract", "declare the referenced contract or fix the id")

	assert.Equal(t, codeREF02, r.Code)
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueRefNotFound, r.IssueType)
	assert.Equal(t, "cells/x/slices/s/slice.yaml", r.File)
	assert.Equal(t, "contractUsages[1].contract", r.Field)
	assert.Equal(t, "references non-existent contract", r.Message)
	assert.Equal(t, "declare the referenced contract or fix the id", r.Fix)
	assert.Equal(t, 5, r.Line, "line should match contractUsages[1].contract")
	assert.Positive(t, r.Column)
}

// TestValidator_NewError_UnknownLocation: when the path cannot be located,
// the result is still valid but Line/Column remain zero.
func TestValidator_NewError_UnknownLocation(t *testing.T) {
	pm := &metadata.ProjectMeta{Cells: map[string]*metadata.CellMeta{}}
	v := NewValidator(pm, "", clock.Real())

	r := v.newError(codeREF01, IssueRefNotFound,
		"cells/x/slice.yaml", "belongsToCell",
		"slice references non-existent cell", "create the cell or fix belongsToCell")

	assert.Equal(t, codeREF01, r.Code)
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

// TestDependencyChecker_NewError_AutoFillsLocation mirrors the Validator
// test to confirm DependencyChecker gets the same location enrichment via
// the embedded locator.
func TestDependencyChecker_NewError_AutoFillsLocation(t *testing.T) {
	src := "id: s\n" + // line 1
		"belongsToCell: ghost\n" + // line 2 — the field we'll locate
		"contractUsages: []\n" // line 3

	pm := &metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
	}
	prepareNode(t, pm, "cells/x/slices/s/slice.yaml", src)
	dc := NewDependencyChecker(pm)

	r := dc.newError(codeDEP01, IssueMismatch,
		"cells/x/slices/s/slice.yaml", "belongsToCell",
		"slice belongsToCell mismatch", "align belongsToCell with the cell that owns this slice")

	assert.Equal(t, 2, r.Line)
	assert.Positive(t, r.Column)
	assert.Equal(t, codeDEP01, r.Code)
}

// TestDependencyChecker_Locate_FallsBack verifies the nil-project / missing
// file nodes fallback shared with Validator.
func TestDependencyChecker_Locate_FallsBack(t *testing.T) {
	dc := NewDependencyChecker(nil)
	// Safe on a nil project (NewDependencyChecker stores it as-is but locate
	// short-circuits on l.project == nil).
	line, col := dc.locate("any.yaml", "id")
	assert.Zero(t, line)
	assert.Zero(t, col)
}

// TestParentFieldPath table-drives the parent walker used by locate's
// fallback. Mistakes here propagate to every rule that points at a
// missing leaf, so lock the corner cases (empty / no separator /
// trailing index / mixed dot+index).
func TestParentFieldPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", ""},
		{"a.b", "a"},
		{"a.b.c", "a.b"},
		{"a.b[0]", "a.b"},
		{"a.b[0].c", "a.b[0]"},
		{"a.b[0][1]", "a.b[0]"},
		{"endpoints.http.responses[401].schemaRef", "endpoints.http.responses[401]"},
		{"endpoints.http.responses[401]", "endpoints.http.responses"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, parentFieldPath(tc.in))
		})
	}
}

// TestValidator_Locate_FallsBackToParentForMissingLeaf is the CH-03
// regression: when a rule fires *because* a leaf field is absent
// (responses[401] declared without a schemaRef), locate must walk up to
// the deepest existing ancestor rather than returning (0, 0). Without
// the fallback, IDE click-to-open and SARIF anchors degrade to
// "file-only" precision and the PR's "carry yaml.Node field-level
// Line/Column" promise breaks for the most common contract-health case.
func TestValidator_Locate_FallsBackToParentForMissingLeaf(t *testing.T) {
	src := "id: demo.v1\n" + // line 1
		"endpoints:\n" + // line 2
		"  http:\n" + // line 3
		"    responses:\n" + // line 4
		"      401:\n" + // line 5 — key
		"        description: unauthorized\n" // line 6 — value's first content; yaml.v3 anchors the mapping value here

	pm := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{},
	}
	prepareNode(t, pm, "contracts/http/demo/v1/contract.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	// The leaf .schemaRef does not exist in the YAML — locator must walk
	// up to the parent responses[401] mapping value, which yaml.v3 places
	// at the first content line (line 6, "description: ..."), not the
	// key line (line 5). One line off the ideal but adjacent — the real
	// improvement is over the previous (0, 0) regression.
	line, col := v.locate(
		"contracts/http/demo/v1/contract.yaml",
		"endpoints.http.responses[401].schemaRef",
	)
	assert.Equal(t, 6, line, "fallback should anchor at responses[401] value's first content line")
	assert.Positive(t, col)
}
