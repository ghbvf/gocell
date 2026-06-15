//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// GoodFrameworkSentinel uses metadata.FrameworkOwnerSentinel at OwnerCell and
// Server — the owner/provider positions where the sentinel is semantically valid
// (#1939). A1 must produce NO findings here: the sentinel is a sanctioned source
// (third form in isSanctionedCellIDExpr) even though it cannot go through
// metadatatest.NewCellID (leading underscore violates MatchCellID).
var GoodFrameworkSentinel = &metadata.ContractMeta{
	ID:               "http.deviceidentity.enroll.v1",
	Kind:             "http",
	OwnerCell:        metadata.FrameworkOwnerSentinel,
	ConsistencyLevel: "L2",
	Lifecycle:        "draft",
	Endpoints: metadata.EndpointsMeta{
		Server: metadata.FrameworkOwnerSentinel,
	},
}

// GoodFrameworkSentinelOtherPositions proves A1's sentinel acceptance is
// position-agnostic (ADR §1a): isSanctionedCellIDExpr matches the sentinel
// expression at ANY cell-id position, not just owner/provider. A CellMeta.ID of
// "_framework" is nonsensical, but A1 deliberately leaves that to FMT-C1 /
// cell-id-pattern rules rather than flagging the sanctioned sentinel expression —
// so A1 must still produce NO findings here. This anti-vacuity case keeps the
// "accepted at all positions" claim from being vacuously green.
var (
	GoodFrameworkSentinelAtID = &metadata.CellMeta{
		ID: metadata.FrameworkOwnerSentinel,
	}
	GoodFrameworkSentinelOtherEndpoints = &metadata.EndpointsMeta{
		Publisher: metadata.FrameworkOwnerSentinel,
		Handler:   metadata.FrameworkOwnerSentinel,
	}
)
