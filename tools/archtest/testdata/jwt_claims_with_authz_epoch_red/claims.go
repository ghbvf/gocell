// Package fixture is the RED fixture for JWT-CLAIMS-NO-AUTHZ-EPOCH-01.
// It declares a Claims-like struct with an AuthzEpoch field AND uses the
// literal "authz_epoch" in a map. The archtest must detect both forms.
//
// This file is NOT compiled by production; it is parsed only as AST data
// by TestJWTClaimsNoAuthzEpoch_RedFixtureDetected to prove the rule catches
// regressions.
package fixture

// Claims mirrors the shape of kernel/cell.Claims pre-S4d, when it still
// carried the AuthzEpoch field. Re-introducing this field in production
// regresses ADR-credential §A8 (row-level SoR).
type Claims struct {
	Subject    string
	AuthzEpoch int64 // S4d: must be removed; this fixture proves the rule catches re-adds.
}

// mintLikeWriter showcases the second regression form: writing the claim
// directly into a map via the literal key. archtest prong 3 catches this.
func mintLikeWriter(m map[string]any, epoch int64) {
	m["authz_epoch"] = epoch
}
