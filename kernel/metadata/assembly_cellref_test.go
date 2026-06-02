package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestAssemblyCellRef_UnmarshalYAML_UnionForms covers the scalar-or-object
// union accepted by AssemblyCellRef.UnmarshalYAML (#1086): bare-string
// shorthand (same-module) and the {id, module} object form (cross-module).
func TestAssemblyCellRef_UnmarshalYAML_UnionForms(t *testing.T) {
	const doc = `
- configcore
- id: payment
  module: github.com/acme/payment-cell
- id: auditcore
`
	var refs []metadata.AssemblyCellRef
	require.NoError(t, yaml.Unmarshal([]byte(doc), &refs))
	require.Len(t, refs, 3)

	assert.Equal(t, "configcore", refs[0].ID)
	assert.Empty(t, refs[0].Module, "scalar shorthand is same-module (empty Module)")

	assert.Equal(t, "payment", refs[1].ID)
	assert.Equal(t, "github.com/acme/payment-cell", refs[1].Module)

	assert.Equal(t, "auditcore", refs[2].ID)
	assert.Empty(t, refs[2].Module, "object form without module is same-module")
}

// TestAssemblyCellRef_UnmarshalYAML_Rejections covers the strict-decode parity
// the custom Unmarshaler enforces itself (KnownFields does not propagate into a
// custom Unmarshaler).
func TestAssemblyCellRef_UnmarshalYAML_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"unknown field", "- {id: x, bogus: y}", "unknown field"},
		{"missing id", "- {module: github.com/acme/x}", "id"},
		{"empty id", `- {id: ""}`, "id"},
		{"non-scalar-non-mapping element", "- [nested, seq]", "must be a string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var refs []metadata.AssemblyCellRef
			err := yaml.Unmarshal([]byte(tc.doc), &refs)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestCellRefsAndCellIDs covers the ergonomic constructor/projection helpers.
func TestCellRefsAndCellIDs(t *testing.T) {
	refs := metadata.CellRefs("configcore", "auditcore")
	require.Len(t, refs, 2)
	assert.Equal(t, "configcore", refs[0].ID)
	assert.Empty(t, refs[0].Module)
	assert.Equal(t, "auditcore", refs[1].ID)

	assert.Equal(t, []string{"configcore", "auditcore"}, metadata.CellIDs(refs))
	assert.Empty(t, metadata.CellIDs(nil))
}
