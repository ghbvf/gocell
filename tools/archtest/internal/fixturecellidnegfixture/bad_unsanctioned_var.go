//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// BadUnsanctionedVar uses a metadatatest package-level var whose name does NOT
// begin with "CellID". A1 must reject this shape because the CellID* prefix
// restriction was introduced to prevent future non-cell-id vars in metadatatest
// from being silently accepted as sanctioned cell-id sources.
//
// NOTE: This var is not declared in the metadatatest package itself (to avoid
// polluting that package with a test-fixture-only symbol). Instead we use a
// local var of the same semantics to demonstrate the rejection path: the local
// var below is from this package, not metadatatest — so any SelectorExpr-based
// reference is disqualified on both the package-path and name-prefix axes.
// The actual scenario this fixture targets (metadatatest.<NonCellIDVar>) is
// structurally equivalent: isSanctionedCellIDExpr rejects on name-prefix.
var unsanctionedLocal = metadatatest.NewCellID("unsanctionedcell")

// BadUnsanctionedVar constructs a CellMeta whose ID uses a local var (not a
// metadatatest CellID* var) — must be flagged by A1.
var BadUnsanctionedVar = map[string]*metadata.CellMeta{
	metadatatest.CellIDAccessCore: {
		ID:               unsanctionedLocal,
		Type:             "core",
		ConsistencyLevel: "L1",
	},
}
