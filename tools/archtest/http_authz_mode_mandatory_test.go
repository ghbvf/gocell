//go:build archtest

// INVARIANT: HTTP-AUTHZ-MODE-MANDATORY-01
//
// #2020 "default ABAC, explicit opt-out": every active codegen HTTP route MUST declare
// an AuthZ mode — the ABAC default (endpoints.http.permission) or an explicit opt-out
// (public/bootstrap/clientsOnly/serviceOwned). The Hard carrier is the cellgen
// generate-time completeness gate (cellgen.validateHTTPAuthModeCompleteness); FMT-42 is
// the governance defense-in-depth layer. This archtest pins the frozen migration ledger
// (metadata.httpAuthModeMigrationLedger) to the real project so the ledger:
//
//   - never goes STALE: a ledgered contract that has migrated (declared a mode) or been
//     removed must be deleted from the ledger — driving the set monotonically to empty;
//   - never silently absorbs a NEW modeless contract: the ledger is FROZEN, so a brand-new
//     route must declare a mode rather than be added here (mirrors the cellgen gate; the
//     size ceiling is additionally pinned by metadata.TestHTTPAuthModeLedger_FrozenSize).
//
// Endgame: once the ledger drains to empty, delete it together with the gate's
// ledger-exemption branch.
package archtest

import (
	"sort"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// diffAuthModeLedger compares the frozen ledger against the project's actual modeless
// set, returning the two drift directions: `stale` (ledgered but no longer modeless —
// migrated/removed) and `uncovered` (modeless but not ledgered — a new route that must
// declare a mode). Pure function so the drift logic has a synthetic red case
// (TestHTTPAuthModeLedger_DiffDetectsDrift) independent of the real-project scan.
func diffAuthModeLedger(ledger, modeless map[string]bool) (stale, uncovered []string) {
	for id := range ledger {
		if !modeless[id] {
			stale = append(stale, id)
		}
	}
	for id := range modeless {
		if !ledger[id] {
			uncovered = append(uncovered, id)
		}
	}
	sort.Strings(stale)
	sort.Strings(uncovered)
	return stale, uncovered
}

func TestHTTPAuthModeLedger_MatchesProjectModeless(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	httpCount := 0
	modeless := map[string]bool{}
	for _, c := range project.Contracts {
		if c.Kind != "http" {
			continue
		}
		httpCount++
		if c.Lifecycle != "active" || !c.Codegen {
			continue
		}
		if !metadata.HTTPAuthModeDeclared(c.Endpoints.HTTP) {
			modeless[c.ID] = true
		}
	}
	// Anti-vacuity: the loader must actually see the project's HTTP contracts, otherwise
	// "ledger == modeless" would pass trivially against an empty set.
	if httpCount == 0 {
		t.Fatal("anti-vacuity: parsed zero http contracts — project loader misconfigured")
	}

	ledger := map[string]bool{}
	for _, id := range metadata.HTTPAuthModeLedgerIDs() {
		ledger[id] = true
	}

	stale, uncovered := diffAuthModeLedger(ledger, modeless)
	for _, id := range stale {
		t.Errorf("ledger entry %q is no longer a modeless active codegen HTTP route — delete it "+
			"from httpAuthModeMigrationLedger in framework/kernel/metadata/authz_mode.go and "+
			"decrement httpAuthModeLedgerFrozenSize (#2020 no-stale)", id)
	}
	for _, id := range uncovered {
		t.Errorf("modeless active HTTP route %q is neither declared nor ledgered — add "+
			"endpoints.http.permission (ABAC default) or an explicit opt-out flag to this "+
			"contract's contract.yaml (#2020); the ledger is frozen to new entries", id)
	}
}

// TestHTTPAuthModeLedger_DiffDetectsDrift is the synthetic red case for the drift logic
// (ai-robust.md: content-scan rules require a synthetic red case + anti-vacuity). It
// proves both drift directions fire independently of the real-project scan.
func TestHTTPAuthModeLedger_DiffDetectsDrift(t *testing.T) {
	t.Parallel()
	ledger := map[string]bool{"http.a.v1": true, "http.b.v1": true}

	// In sync → no drift (anti-vacuity floor: identical sets must produce empty diff).
	if stale, uncovered := diffAuthModeLedger(ledger, map[string]bool{"http.a.v1": true, "http.b.v1": true}); len(stale)+len(uncovered) != 0 {
		t.Fatalf("identical sets must not drift, got stale=%v uncovered=%v", stale, uncovered)
	}

	// b migrated (declared a mode) → stale; new c appeared modeless+unledgered → uncovered.
	stale, uncovered := diffAuthModeLedger(ledger, map[string]bool{"http.a.v1": true, "http.c.v1": true})
	if len(stale) != 1 || stale[0] != "http.b.v1" {
		t.Errorf("expected stale=[http.b.v1] (ledgered but migrated), got %v", stale)
	}
	if len(uncovered) != 1 || uncovered[0] != "http.c.v1" {
		t.Errorf("expected uncovered=[http.c.v1] (new modeless, unledgered), got %v", uncovered)
	}
}
