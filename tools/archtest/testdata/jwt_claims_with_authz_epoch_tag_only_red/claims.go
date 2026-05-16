// Package fixture is a tag-only RED fixture for JWT-CLAIMS-NO-AUTHZ-EPOCH-01
// prong 3b.
//
// This form would bypass the original literal-scan in prong 3: the field name
// is "Epoch" (not "AuthzEpoch"), so prong 1's field-name check misses it; and
// the struct tag ` + "`" + `json:"authz_epoch"` + "`" + ` is a raw string literal whose full
// BasicLit.Value is ` + "`" + `` + "`" + `json:"authz_epoch"` + "`" + `` + "`" + ` — not the bare "authz_epoch"
// literal that the original prong 3 scan matched. The StructTagJSONKey helper
// in internal/scanner/literal.go parses the tag correctly and detects this form.
//
// This file is NOT compiled by production; it is parsed only as AST data
// by TestJWTClaimsNoAuthzEpoch_TagOnlyRedFixtureDetected to prove the rule
// catches the tag-only regression.
package fixture

// Claims demonstrates the tag-only regression: re-introducing authz_epoch
// as the JSON key of a field with a different Go name. Pre-fix, this form
// evaded prong 3's literal scan because the BasicLit.Value was the full
// backtick-delimited tag string, not the bare key.
type Claims struct {
	Subject string
	// Epoch carries the authz_epoch JSON key even though the Go field name
	// is "Epoch" — prong 1 (AuthzEpoch field name) would NOT catch this.
	Epoch int64 `json:"authz_epoch"`
}
