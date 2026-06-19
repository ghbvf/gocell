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
//     route must declare a mode rather than be added here (mirrors the cellgen gate).
//
// Endgame: once the ledger drains to empty, delete it together with the gate's
// ledger-exemption branch.
package archtest

import (
	"sort"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

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

	var stale []string
	for id := range ledger {
		if !modeless[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	for _, id := range stale {
		t.Errorf("ledger entry %q is no longer a modeless active codegen HTTP route — delete it "+
			"from httpAuthModeMigrationLedger (#2020 no-stale)", id)
	}

	var uncovered []string
	for id := range modeless {
		if !ledger[id] {
			uncovered = append(uncovered, id)
		}
	}
	sort.Strings(uncovered)
	for _, id := range uncovered {
		t.Errorf("modeless active HTTP route %q is neither declared nor ledgered — declare "+
			"endpoints.http.permission or an explicit opt-out (#2020); the ledger is frozen to new entries", id)
	}
}
