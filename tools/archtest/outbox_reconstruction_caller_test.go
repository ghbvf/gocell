// INVARIANT: OUTBOX-RECONSTRUCTION-CALLER-01
//
// This _test.go only dogfoods + runs the anti-vacuity reverse self-check. The
// importable rule body (CheckOutboxReconstructionCaller01 +
// collectReconstructionCallerViolations + observeReconstructionCallerRefs +
// checkReconstructionAntiVacuity + the type-aware resolution helpers + the full
// package godoc) lives in the non-test companion
// outbox_reconstruction_caller.go, so external Cell repos can import and run it
// via StandardCellRules / RunStandardCellRules (M3 #1302 / #1635).
package archtest

import (
	"testing"
)

// TestOutboxReconstructionCaller01 dogfoods OUTBOX-RECONSTRUCTION-CALLER-01
// against GoCell itself by calling the same CheckOutboxReconstructionCaller01
// that StandardCellRules (and external Cell repos via RunStandardCellRules) use
// — single source, no parallel rule body.
//
// The anti-vacuity / no-stale reverse self-check is run separately in
// TestOutboxReconstructionCaller01_AntiVacuity to keep concerns distinct and
// mirror the scaffold_derived_forceoverwrite pattern.
func TestOutboxReconstructionCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, ruleOutboxReconstructionCaller01,
		CheckOutboxReconstructionCaller01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestOutboxReconstructionCaller01_AntiVacuity runs the anti-vacuity /
// no-stale reverse self-check: every allowlisted file in
// reconstructionFunnelAllowlist must host a live reference to its funnel. A
// stale entry is a latent bypass slot (charter "no silent carve-over / empty
// steady state"). This check also proves the scanner actually resolves the real
// references (not a vacuous pass), because a scanner bug would zero the observed
// map and make every allowlist entry appear stale.
func TestOutboxReconstructionCaller01_AntiVacuity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Accumulate observed (symbol → file) across the full production scan.
	merged := map[string]map[string]struct{}{}
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		obs := observeReconstructionCallerRefs(p)
		for symbol, files := range obs {
			if merged[symbol] == nil {
				merged[symbol] = map[string]struct{}{}
			}
			for f := range files {
				merged[symbol][f] = struct{}{}
			}
		}
		return nil
	})

	Report(t, ruleOutboxReconstructionCaller01, checkReconstructionAntiVacuity(merged))
}
