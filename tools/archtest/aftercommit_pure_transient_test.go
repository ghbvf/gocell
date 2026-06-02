// INVARIANT: AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
//
// aftercommit_pure_transient_test.go — test entry points for
// AFTERCOMMIT-HOOK-PURE-TRANSIENT-01. Rule logic, consts, and shared scan
// helpers live in aftercommit_pure_transient_invariants.go (the non-test file,
// so external Cell repos can compile and call it directly — it is NOT in
// StandardCellRules; see that file's godoc for why). This file dogfoods those
// shared helpers against GoCell itself — single source, no parallel rule body.
//
// Production dogfood: one-liner via CheckAfterCommitHookPureTransient.
// Fixture/RED tests: Run(t, Fixture(...)) against testdata/ fixture dirs, reusing
// the shared per-Pass scan helpers from the non-test file.
package archtest

import (
	"path/filepath"
	"testing"
)

// TestAfterCommitHookPureTransient is the production dogfood: runs
// AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 against GoCell itself via the shared
// CheckAfterCommitHookPureTransient entry point (single source).
func TestAfterCommitHookPureTransient(t *testing.T) {
	t.Parallel()
	Report(t, ruleAfterCommitHookPureTransient,
		CheckAfterCommitHookPureTransient(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestAfterCommitHookPureTransient_A1A2_Fixtures runs A1 + A2 over the
// intentional-violation fixtures and asserts the diagnostic set via golden.
func TestAfterCommitHookPureTransient_A1A2_Fixtures(t *testing.T) {
	for _, fix := range []string{
		"green",
		"red_non_literal",
		"red_tx_exec",
		"red_outbox_write",
		"red_local_helper",
		"red_cellwriter",
	} {
		fix := fix
		t.Run("fixture_"+fix, func(t *testing.T) {
			root := findModuleRoot(t)
			relDir, pattern := afterCommitFixturePattern(fix)
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil {
					return nil
				}
				localFuncs := packageFuncDecls(p)
				ifaces := resolveBannedReceivers(p.Pkg)
				var out []Diagnostic
				for _, file := range p.Files {
					out = append(out, scanRegisterAfterCommitHooks(p, file, localFuncs, ifaces)...)
				}
				return out
			})

			AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
		})
	}
}

// TestAfterCommitHookPureTransient_A3_Fixture asserts a non-allowlisted caller
// of the registry plumbing funcs is flagged.
func TestAfterCommitHookPureTransient_A3_Fixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := afterCommitFixturePattern("red_unallowlisted_drain")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanAfterCommitDrainCallers(p, file)...)
		}
		return out
	})

	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestAfterCommitHookPureTransient_Escapes_Fixture asserts the B2/B3 escape scan
// (incl. the F4 generic-callee unwrap) flags a hook reaching for the committed
// tx via persistence.TxFromContext[pgx.Tx].
func TestAfterCommitHookPureTransient_Escapes_Fixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := afterCommitFixturePattern("red_txfromcontext")
	diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRegisterAfterCommitEscapes(p, file)...)
		}
		return out
	})

	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestAfterCommitHookPureTransient_BlindSpots_NoEscapeInProduction is the
// reverse self-test for blind spots B2 (TxFromContext capture) and B3
// (reflection MethodByName) inside production hook bodies. With no production
// hooks yet the set is empty; the check bites when saga adds hooks.
func TestAfterCommitHookPureTransient_BlindSpots_NoEscapeInProduction(t *testing.T) {
	diags := Run(t, Production(TypedOpts{Tags: FlatNonDefaultTags()}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRegisterAfterCommitEscapes(p, file)...)
		}
		return out
	})

	Report(t, ruleAfterCommitHookPureTransient+"-BLINDSPOT", diags)
}
