//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// GoodConstRef uses metadatatest pre-validated cell-id constants at
// every cell-id position — A1 must produce no findings here.
var GoodConstRef = &metadata.ProjectMeta{
	Cells: map[string]*metadata.CellMeta{
		metadatatest.CellIDAccessCore: {
			ID:               metadatatest.CellIDAccessCore,
			Type:             "core",
			ConsistencyLevel: "L2",
			L0Dependencies: []metadata.L0DepMeta{
				{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
			},
		},
	},
	Journeys: map[string]*metadata.JourneyMeta{
		"J-good": {
			ID:    "J-good",
			Cells: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
		},
	},
}
