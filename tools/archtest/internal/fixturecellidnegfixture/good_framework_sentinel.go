//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// GoodFrameworkSentinel uses metadata.FrameworkOwnerSentinel at OwnerCell and
// Server — the two positions where the sentinel is semantically valid (#1939).
// A1 must produce NO findings here: the sentinel is a sanctioned source
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
