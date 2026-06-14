//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// GoodCallLiteral uses metadatatest.NewCellID(BasicLit) at every
// cell-id position — A1 must produce no findings here.
var GoodCallLiteral = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		metadatatest.NewCellID("freshid"): {
			ID:               metadatatest.NewCellID("freshid"),
			Type:             "core",
			ConsistencyLevel: "L2",
			L0Dependencies: []metadata.L0DepMeta{
				{Cell: metadatatest.NewCellID("anotherone"), Reason: "test"},
			},
		},
	},
}
