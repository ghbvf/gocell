//go:build archtest

// fence_token_mint_funnel_test.go — dogfoods CheckFenceTokenMintFunnel against
// GoCell itself.
//
//   - INVARIANT: FENCE-TOKEN-MINT-FUNNEL-01
//
// Detector logic, allowlist, and Check* function live in
// fence_token_mint_funnel.go (non-test) so they can be compiled by
// external Cell repositories. This file dogfoods that shared logic against
// GoCell itself — single source, no parallel rule body.
package archtest

import (
	"testing"
)

// TestFenceTokenMintFunnel_AllowlistEnforced enforces FENCE-TOKEN-MINT-FUNNEL-01.
func TestFenceTokenMintFunnel_AllowlistEnforced(t *testing.T) {
	t.Parallel()
	Report(t, "FENCE-TOKEN-MINT-FUNNEL-01",
		CheckFenceTokenMintFunnel(t, ConfigForExternalCell{}))

	// Reverse RED self-check: the scanner must catch all five reference forms
	// (call / var-decl capture / short-var capture / pass-through arg /
	// reflect arg) in the external_mint_red fixture. wantMin=5 pins
	// form-completeness — a CallExpr-only scanner would catch only the single
	// direct call and fail here.
	verifyFenceTokenMintRedFixture(t,
		"./tools/archtest/testdata/fence_token_fixtures/external_mint_red",
		"FENCE-TOKEN-MINT-FUNNEL-01 RED fixture", 5)
}
