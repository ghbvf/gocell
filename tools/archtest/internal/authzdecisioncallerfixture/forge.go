//go:build archtest_fixture

// Package authzdecisioncallerfixture is a RED fixture for
// AUTHZ-DECISION-ALLOW-DENY-CALLER-01. It references authz.Allow / authz.Deny
// from a package that is NOT on allowDenyCallerAllowlist, so the use-based
// detector must flag both references. It is never imported by production code;
// it exists only so the archtest reverse self-check can prove the detector fires.
//
// Gated behind the archtest_fixture build tag so it is invisible to the
// Production() scan (which would otherwise flag it as an unsanctioned caller) but
// loaded by the Fixture() façade in the reverse self-check.
package authzdecisioncallerfixture

import "github.com/ghbvf/gocell/pkg/authz"

// ForgeAllow forges an Allow verdict outside the sanctioned PDP engine (RED).
func ForgeAllow() authz.Decision {
	dec, _ := authz.Allow(authz.Obligations{})
	return dec
}

// ForgeDeny forges a Deny verdict outside the sanctioned PDP engine (RED).
func ForgeDeny() authz.Decision {
	return authz.Deny("forged")
}
