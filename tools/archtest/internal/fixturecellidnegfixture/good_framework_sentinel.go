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

// GoodFrameworkSentinelOtherEndpoints exercises the remaining PROVIDER-endpoint
// positions where the sentinel is sanctioned — EndpointsMeta.Publisher (event) and
// Handler (command). A1 must produce NO findings here. (The sentinel at the
// NON-provider position CellMeta.ID is the opposite, RED case — see
// bad_framework_sentinel_at_id.go.) Together the good/bad pair proves A1 is
// field-position-aware (ADR §1a), not vacuously green in either direction.
var GoodFrameworkSentinelOtherEndpoints = &metadata.EndpointsMeta{
	Publisher: metadata.FrameworkOwnerSentinel,
	Handler:   metadata.FrameworkOwnerSentinel,
}
