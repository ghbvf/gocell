//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// BadField uses a sanctioned map key (metadatatest constant) but a bare
// literal in CellMeta.ID and L0DepMeta.Cell — must be flagged by A1
// (direct field positions).
var BadField = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		metadatatest.CellIDAccessCore: {
			ID:               "bareliteralid",
			Type:             "core",
			ConsistencyLevel: "L1",
			L0Dependencies: []metadata.L0DepMeta{
				{Cell: "barelitedep", Reason: "test"},
			},
		},
	},
}
