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
	prepareNode(t, pm, "corecells/accesscore/cell.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	r := v.newWarning(codeREF01, IssueRefNotFound,
		"corecells/accesscore/cell.yaml", "id",
		"advisory: cell id unreferenced by any journey",
		"reference the cell from a journey or remove it if unused")

	assert.Equal(t, codeREF01, r.Code)
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.Equal(t, IssueRefNotFound, r.IssueType)
	assert.Equal(t, "corecells/accesscore/cell.yaml", r.File)
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
	prepareNode(t, pm, "corecells/accesscore/cell.yaml", src)
	v := NewValidator(pm, "", clock.Real())

	line, col := v.locate("corecells/accesscore/cell.yaml", "id")
	assert.Equal(t, 1, line, "id line")
	assert.Positive(t, col, "id column")

	line, col = v.locate("corecells/accesscore/cell.yaml", "owner.team")
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

// TestLocator_NewError_AutoFillsLocation confirms findings constructed through
// the locator embedded in Validator get line/column enrichment from the
// yaml.Node cache (the DEP rules rely on this for belongsToCell findings).
func TestLocator_NewError_AutoFillsLocation(t *testing.T) {
	src := "id: s\n" + // line 1
		"belongsToCell: ghost\n" + // line 2 — the field we'll locate
		"contractUsages: []\n" // line 3

	pm := &metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
	}
	prepareNode(t, pm, "cells/x/slices/s/slice.yaml", src)
	dc := NewValidator(pm, "", clock.Real())

	r := dc.newError(codeDEP01, IssueMismatch,
		"cells/x/slices/s/slice.yaml", "belongsToCell",
		"slice belongsToCell mismatch", "align belongsToCell with the cell that owns this slice")

	assert.Equal(t, 2, r.Line)
	assert.Positive(t, r.Column)
	assert.Equal(t, codeDEP01, r.Code)
}

// TestLocator_Locate_FallsBack verifies the missing file-nodes fallback on the
// locator embedded in Validator.
func TestLocator_Locate_FallsBack(t *testing.T) {
	v := NewValidator(nil, "", clock.Real())
	// NewValidator substitutes an empty ProjectMeta; locate returns 0,0 when the
	// file has no yaml.Node cache entry.
	line, col := v.locate("any.yaml", "id")
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

// TestCanonicalCellID_Corecells verifies that canonicalCellID recognizes the
// corecells flat layout (corecells/<id>/cell.yaml) and still accepts the
// conventional cells/ layout. Both must return the correct cell ID. Corecells
// coverage matters because the resolveFile path in locator.go dispatches
// through this function, and a mis-classified corecells path silently falls
// through to the raw-file fallback, producing (0,0) locations.
func TestCanonicalCellID_Corecells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file   string
		wantID string
		wantOK bool
	}{
		{"corecells/accesscore/cell.yaml", "accesscore", true},
		{"corecells/auditcore/cell.yaml", "auditcore", true},
		{"cells/billing/cell.yaml", "billing", true},
		// Wrong segment count or missing cell.yaml → no match.
		{"corecells/cell.yaml", "", false},
		{"corecells/accesscore/slices/s/cell.yaml", "", false},
		{"contracts/http/foo/v1/contract.yaml", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			id, ok := canonicalCellID(tc.file)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch for %q", tc.file)
			if ok {
				assert.Equal(t, tc.wantID, id, "id mismatch for %q", tc.file)
			}
		})
	}
}

// TestCanonicalSliceKey_Corecells verifies that canonicalSliceKey recognizes
// the corecells flat layout (corecells/<cell>/slices/<slice>/slice.yaml) and
// still accepts the conventional cells/ layout. Both must return the correct
// "<cellID>/<sliceID>" composite key used by sliceMetaFile to look up SliceMeta.
func TestCanonicalSliceKey_Corecells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file    string
		wantKey string
		wantOK  bool
	}{
		{"corecells/accesscore/slices/sessionlogin/slice.yaml", "accesscore/sessionlogin", true},
		{"corecells/auditcore/slices/auditappend/slice.yaml", "auditcore/auditappend", true},
		{"cells/billing/slices/query/slice.yaml", "billing/query", true},
		// Wrong segment count → no match.
		{"corecells/accesscore/slice.yaml", "", false},
		{"corecells/accesscore/slices/slice.yaml", "", false},
		{"contracts/http/foo/v1/contract.yaml", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			key, ok := canonicalSliceKey(tc.file)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch for %q", tc.file)
			if ok {
				assert.Equal(t, tc.wantKey, key, "key mismatch for %q", tc.file)
			}
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
