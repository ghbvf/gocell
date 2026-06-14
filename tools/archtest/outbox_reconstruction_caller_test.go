//go:build archtest

// INVARIANT: OUTBOX-RECONSTRUCTION-CALLER-01
//
// This _test.go dogfoods the rule, runs the anti-vacuity reverse self-check,
// unit-tests the platform-identity allowlist bind (TestIsReconstructionAllowedSite),
// and gates the dot-import bare-Ident blind-spot closure with a RED fixture
// (TestOutboxReconstructionCaller01_DotImportFixture). The importable rule body
// (CheckOutboxReconstructionCaller01 +
// collectReconstructionCallerViolations + observeReconstructionCallerRefs +
// checkReconstructionAntiVacuity + the type-aware resolution helpers + the full
// package godoc) lives in the non-test companion
// outbox_reconstruction_caller.go, so external Cell repos can import and run it
// via StandardCellRules / RunStandardCellRules (M3 #1302 / #1635).
package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsReconstructionAllowedSite proves the reconstruction allowlist is bound to
// PLATFORM package identity, not just a repo-relative path. The critical case is
// "consumer module forges allowlisted rel path": a consumer that recreates
// runtime/eventbus/eventbus.go must STILL be flagged (false), because its package
// path is not under PlatformModulePath — closing the registered rule's pure-ban
// bypass (codex #1682 F2).
func TestIsReconstructionAllowedSite(t *testing.T) {
	t.Parallel()
	const (
		unmarshal = "kernel/outbox.UnmarshalEnvelope"
		toEntry   = "(kernel/outbox.EntryScan).ToEntry"
	)
	cases := []struct {
		name        string
		pkgPath     string
		symbol, rel string
		want        bool
	}{
		{
			"sanctioned platform wire-decode site", PlatformFrameworkModulePath + "/runtime/eventbus",
			unmarshal, "runtime/eventbus/eventbus.go", true,
		},
		{"sanctioned platform storage site", PlatformModulePath + "/adapters/postgres", toEntry, "adapters/postgres/outbox_store.go", true},
		{"consumer module forges allowlisted rel", "consumer.example/app/runtime/eventbus", unmarshal, "runtime/eventbus/eventbus.go", false},
		{"platform pkg, non-allowlisted rel", PlatformModulePath + "/cells/foo", unmarshal, "cells/foo/cell.go", false},
		{
			"platform pkg, unknown symbol", PlatformFrameworkModulePath + "/runtime/eventbus",
			"kernel/outbox.Bogus", "runtime/eventbus/eventbus.go", false,
		},
		{"unresolved pkg", "", unmarshal, "runtime/eventbus/eventbus.go", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isReconstructionAllowedSite(tc.pkgPath, tc.symbol, tc.rel); got != tc.want {
				t.Errorf("isReconstructionAllowedSite(%q, %q, %q) = %v, want %v",
					tc.pkgPath, tc.symbol, tc.rel, got, tc.want)
			}
		})
	}
}

// TestOutboxReconstructionCaller01_DotImportFixture is the RED-fixture gate for
// the dot-import bare-identifier blind spot (codex #1682 F5): a non-sanctioned
// file that dot-imports kernel/outbox and calls UnmarshalEnvelope as a bare
// *ast.Ident must be flagged by the forward scan's Ident branch. Without that
// branch the call is invisible and this asserts the regression by going green
// only when the Ident walk fires.
func TestOutboxReconstructionCaller01_DotImportFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const fixturePattern = "./tools/archtest/testdata/reconstruction_caller_fixtures/red_dot_import"
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
		diags = append(diags, collectReconstructionCallerViolations(p)...)
		return nil
	})

	assert.NotEmpty(t, diags,
		"OUTBOX-RECONSTRUCTION-CALLER-01 dot-import fixture: expected ≥1 diagnostic for the "+
			"bare-identifier UnmarshalEnvelope call; the Ident-walk blind-spot closure regressed")
	for _, d := range diags {
		assert.Equal(t, "tools/archtest/testdata/reconstruction_caller_fixtures/red_dot_import/usage.go", d.Rel,
			"diagnostic must carry the structured fixture rel path")
		assert.True(t, strings.Contains(d.Message, "kernel/outbox.UnmarshalEnvelope"),
			"diagnostic must name the UnmarshalEnvelope funnel symbol; got %q", d.Message)
	}
}

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
