//go:build archtest_fixture

package redsagaslogattrliteral

import "log/slog"

// keyedIdentityAttr builds an identity attr as a keyed slog.Attr composite
// literal. B4 must flag the keyed form.
func keyedIdentityAttr() slog.Attr {
	return slog.Attr{Key: "instance_id", Value: slog.StringValue("inst-x")}
}

// unkeyedIdentityAttr builds an identity attr as an UNKEYED (positional)
// slog.Attr composite literal — Key is the first struct field. B4 must flag the
// unkeyed form too (#1266 review C1/F2).
func unkeyedIdentityAttr() slog.Attr {
	return slog.Attr{"lease_id", slog.StringValue("lease-x")}
}
