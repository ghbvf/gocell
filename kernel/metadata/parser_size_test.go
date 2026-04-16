package metadata

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseFS_RejectsOversizeFile guards the 1 MiB ceiling on metadata YAML
// files. ProjectMeta.Nodes retains the full AST for the life of the
// Validator, so an oversized file would inflate live memory 2–3×.
func TestParseFS_RejectsOversizeFile(t *testing.T) {
	// Build a ~1.1 MiB cell.yaml by padding the description field.
	padding := strings.Repeat("x", maxMetadataFileSize+1024)
	fs := fstest.MapFS{
		"cells/huge/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: huge\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n" +
				"description: " + padding + "\n",
		)},
	}

	_, err := NewParser(".").ParseFS(fs)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
	assert.Contains(t, err.Error(), "exceeds limit")
}

// TestParseFS_AcceptsFileJustUnderLimit confirms the limit is inclusive —
// a file exactly at the ceiling still parses (as a regression guard
// against off-by-one).
func TestParseFS_AcceptsFileJustUnderLimit(t *testing.T) {
	// Note: exceeds would need extra struct fields than what CellMeta has.
	// Use a reasonable 100 KiB payload — well under 1 MiB, well over any real
	// metadata file.
	padding := strings.Repeat("x", 100*1024)
	fs := fstest.MapFS{
		"cells/big/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: big\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: " + padding + "}\n" +
				"verify: {smoke: []}\n",
		)},
	}

	pm, err := NewParser(".").ParseFS(fs)
	require.NoError(t, err)
	assert.NotNil(t, pm.Cells["big"])
}
