package outboxtest

import (
	"context"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// principalRoundTripCreatedAt is a fixed UTC seal time for the principal
// round-trip seed. The specific instant is arbitrary — any constant works;
// it is pinned only so the assertion is deterministic and independent of the
// CI runner clock (the seed has no nextRetryAt, so absolute time never affects
// claimability).
var principalRoundTripCreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// principalRoundTripSkew offsets OccurredAt behind CreatedAt so the conformance
// proves the two timestamps are carried as independent fields (ADR-1042), not
// collapsed onto a single column.
const principalRoundTripSkew = -90 * time.Minute

// RunPrincipalRoundTripConformance asserts that an outbox.Store preserves the
// Principal family (actor/subject/tenant/session) AND the producer-domain
// OccurredAt across a persist → ClaimPending round-trip, and that OccurredAt
// stays distinct from CreatedAt.
//
// It is the spine for issue #1291 FP2 — the conformance the ADR-1042
// implementation matrix names. It is invoked from inside
// RunStoreConformanceSuite, so every store impl that runs the mandatory suite
// (FakeStore here, PGOutboxStore under //go:build integration) covers principal
// round-trip automatically — no opt-in call site a future Store impl could
// silently skip. FP7 extends it with persistence-layer branch cases (empty
// principal, oversize JSONB drop+warn, validation reject).
//
// The seed Entry is built via mustEntry → EntryScan.ToEntry (the sealed
// reconstruction funnel), so the harness populates Principal without touching
// the funnel-locked ctxkeys.With*ID setters.
func RunPrincipalRoundTripConformance(t *testing.T, factory StoreFactory) {
	t.Helper()

	created := principalRoundTripCreatedAt
	occurred := created.Add(principalRoundTripSkew)
	want := kout.PrincipalMetadata{
		ActorID:   "actor-imp",
		SubjectID: "subject-eu",
		TenantID:  "tenant-a",
		SessionID: "sess-42",
	}
	seed := outbox.ClaimedEntry{
		Entry: mustEntry(kout.EntryScan{
			ID:         "principal-roundtrip",
			EventType:  "user.login",
			Topic:      "user.login",
			Payload:    []byte(`{"action":"login"}`),
			CreatedAt:  created,
			OccurredAt: occurred,
			Principal:  want,
		}),
	}

	store := factory(t, []outbox.ClaimedEntry{seed})

	claimed, err := store.ClaimPending(context.Background(), 10)
	if err != nil {
		t.Fatalf(msgClaimPending, err)
	}
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}

	got := claimed[0]
	if gotP := got.Principal(); gotP != want {
		t.Errorf("principal mismatch: got %+v, want %+v", gotP, want)
	}
	if !got.OccurredAt().Equal(occurred) {
		t.Errorf("OccurredAt mismatch: got %v, want %v", got.OccurredAt(), occurred)
	}
	if !got.CreatedAt().Equal(created) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", got.CreatedAt(), created)
	}
	if got.OccurredAt().Equal(got.CreatedAt()) {
		t.Errorf("OccurredAt and CreatedAt must remain distinct, both = %v", got.CreatedAt())
	}
}
