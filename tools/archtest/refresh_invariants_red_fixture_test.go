// invariants:
//   - INVARIANT: REFRESH-CROSS-STORE-TX-01 (RED fixture coverage)
package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRefreshCrossStoreTX01_RedFixtureDetected asserts the production rule
// (scanRefreshCrossStoreTX) catches both banned call shapes in the fixture
// at tools/archtest/internal/refreshinvariantsfixture/refresh_red.go (gated
// by `//go:build archtest_fixture`):
//
//   - s.refreshStore.Peek(...)    outside RunInTx closure
//   - s.sessionStore.Get(...)     outside RunInTx closure
//
// # Why a RED fixture is required
//
// Without a fixture, the production rule's zero-diagnostic outcome on real
// sessionrefresh.Service.Refresh carries no information: the rule could
// be silently broken AND the production code happens to comply. The
// fixture provides a known-positive sample so an accidental rule
// regression turns this test red.
//
// # Coverage rationale (Soft → Medium difference exposed)
//
// The Soft predecessor used a hand-coded "s.<field>.<method>" Ident-name
// match keyed by a guardedCalls map that listed `sessionRepo.Update` and
// `sessionRepo.GetByID` — both stale post-PR #482 (cell-private
// SessionRepository was deleted; sessionrefresh now consumes
// runtime/auth/session.Store). `sessionStore.Get` was not in the map.
// Soft would catch `s.refreshStore.Peek` (1 hit) but miss
// `s.sessionStore.Get` (0 hits).
//
// The Medium upgrade (T3 Wave 2) uses archtest.ResolveMethodCall to
// identify methods by their owning interface + named receiver, then
// matches against refreshGuardedMethods which includes
// (session, Store, Get). The fixture must yield exactly 2 hits: both
// outside-closure calls.
func TestRefreshCrossStoreTX01_RedFixtureDetected(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false, Tags: []string{"archtest_fixture"}},
		[]string{"./tools/archtest/internal/refreshinvariantsfixture/..."},
		scanRefreshCrossStoreTX,
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Exact equality (not ≥ 2) — the fixture is intentionally minimal so a
	// future change must explicitly bump the expected count.
	assert.Len(t, diags, 2,
		"fixture must yield exactly 2 REFRESH-CROSS-STORE-TX-01 hits "+
			"(refreshStore.Peek + sessionStore.Get outside RunInTx closure); "+
			"if the fixture changes intentionally, update the expected count")

	// Strong specificity check: the rule MUST catch sessionStore.Get
	// (the post-PR #482 lookup-chain entry the Soft predecessor missed).
	var sawSessionStoreGet bool
	for _, d := range diags {
		if strings.Contains(d.Message, "session.Store.Get") {
			sawSessionStoreGet = true
			break
		}
	}
	assert.True(t, sawSessionStoreGet,
		"REFRESH-CROSS-STORE-TX-01 must flag sessionStore.Get outside the closure — "+
			"this is the new lookup-chain entry added in PR #482 that the stale "+
			"sessionRepo.GetByID guardedCalls entry no longer covers")
}
