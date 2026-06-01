// Package idempotencytest provides a reusable conformance suite for
// implementations of [idemhttp.Store]. Any implementation — in-memory
// (Batch 1) or Redis-backed (adapters/redis) — calls RunConformanceSuite to
// verify the full Claim/Record/Release/TTL contract.
//
// The suite is a plain .go file (not _test.go) so it can be imported by test
// packages in other layers without being stripped from the build graph.
package idempotencytest

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// TimeAdvancer allows the conformance suite to move time forward past a TTL
// boundary without depending on the specific clock implementation used by a
// given Store backend.
//
//   - For MemStore, the factory wraps [clockmock.FakeClock.Advance].
//   - For Redis-backed stores, the factory uses a real time.Sleep so Redis
//     server-side PX expiry fires naturally.
//
// AdvancePast advances time by at least d so that any TTL of exactly d has
// already expired. Implementations must block until the advance is complete.
type TimeAdvancer interface {
	AdvancePast(d time.Duration)
}

// Factory constructs a fresh Store, a TimeAdvancer that controls the TTL
// clock for that store, and a cleanup function. cleanup is safe to call once
// and must be deferred by the caller.
//
// The TimeAdvancer returned by the factory is the SAME clock the Store uses
// internally (for MemStore: a *clockmock.FakeClock wrapper; for Redis: a
// real-sleep wrapper), so tests can expire leases without busy-waiting.
type Factory func(t *testing.T) (store idemhttp.Store, adv TimeAdvancer, cleanup func())

// Package-level constants extracted per coding-standards duplication rules.
const (
	conformNS     = "conf-ns"
	conformKey    = "conf-key-001"
	conformAltNS  = "conf-alt-ns"
	conformAltKey = "conf-alt-key-001"
	shortLeaseTTL = 50 * time.Millisecond
	shortDoneTTL  = 50 * time.Millisecond
	// conformLeaseTTL is a normal-length lease TTL used in conformance cases
	// that do not exercise TTL expiry.
	conformLeaseTTL = 30 * time.Second
	// conformDoneTTL is the done-state TTL used when recording a response in
	// conformance cases.
	conformDoneTTL = 24 * time.Hour
)

// RunConformanceSuite runs the full Store conformance suite against the
// supplied factory. Every subtest is registered as a t.Run so individual
// cases can be filtered with -run.
func RunConformanceSuite(t *testing.T, factory Factory) {
	t.Helper()

	cases := []struct {
		name string
		run  func(*testing.T, Factory)
	}{
		{"FirstClaim_ReturnsAcquired", conformFirstClaimAcquired},
		{"ClaimDone_AfterRecord", conformClaimDoneAfterRecord},
		{"ClaimBusy_WhileLeaseHeld", conformClaimBusyWhileLeaseHeld},
		{"Release_AllowsReClaim", conformReleaseAllowsReClaim},
		{"LeaseTTLExpiry_AllowsReClaim", conformLeaseTTLExpiry},
		{"StaleToken_RecordReturnsError", conformStaleTokenRecord},
		{"DifferentNamespaceKey_AreIndependent", conformDifferentNsKeyIndependent},
		{"DoneTTLExpiry_AllowsReClaim", conformDoneTTLExpiry},
		{"StaleToken_ReleaseAfterExpiry", conformStaleTokenRelease},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { tc.run(t, factory) })
	}
}

// ---------------------------------------------------------------------------
// Conformance cases
// ---------------------------------------------------------------------------

// conformFirstClaimAcquired verifies the initial Claim returns ClaimAcquired
// with a nil recorded response and a non-nil receipt.
func conformFirstClaimAcquired(t *testing.T, factory Factory) {
	t.Helper()
	store, _, cleanup := factory(t)
	defer cleanup()

	state, rec, receipt, err := store.Claim(context.Background(), conformNS, conformKey, conformLeaseTTL)
	if err != nil {
		t.Fatalf("Claim: unexpected error: %v", err)
	}
	if state != idempotency.ClaimAcquired {
		t.Errorf("Claim state = %v, want ClaimAcquired", state)
	}
	if rec != nil {
		t.Errorf("Claim: recorded response must be nil for ClaimAcquired, got non-nil")
	}
	if receipt == nil {
		t.Error("Claim: receipt must be non-nil for ClaimAcquired")
	}
}

// conformClaimDoneAfterRecord acquires a lease, records a response, then
// verifies that a subsequent Claim returns ClaimDone with the stored response.
// It asserts the full round-trip: status, body, and a representative header
// value (Content-Type) must survive the Store → replay cycle unchanged.
// Store implementations that drop body or headers will fail here.
func conformClaimDoneAfterRecord(t *testing.T, factory Factory) {
	t.Helper()
	store, _, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()
	state, _, receipt, err := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("Claim: state=%v err=%v; want ClaimAcquired nil", state, err)
	}

	wantBody := []byte(`{"id":"abc"}`)
	wantContentType := "application/json"
	resp := buildTestResponse(t, 201, wantBody)
	if err := receipt.Record(ctx, &resp, conformDoneTTL); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Second Claim must return ClaimDone with the stored response.
	state2, rec2, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil {
		t.Fatalf("second Claim: unexpected error: %v", err2)
	}
	if state2 != idempotency.ClaimDone {
		t.Errorf("second Claim state = %v, want ClaimDone", state2)
	}
	if rec2 == nil {
		t.Fatal("second Claim: recorded response must be non-nil for ClaimDone")
	}
	if rec2.Status() != 201 {
		t.Errorf("replayed status = %d, want 201", rec2.Status())
	}
	// Body round-trip: Store implementations that drop the body currently pass
	// a status-only check but fail here.
	if !bytes.Equal(rec2.Body(), wantBody) {
		t.Errorf("replayed body = %q, want %q", rec2.Body(), wantBody)
	}
	// Header round-trip: a representative header value (Content-Type) must
	// survive the Store → replay cycle.
	if got := rec2.Header().Get("Content-Type"); got != wantContentType {
		t.Errorf("replayed Content-Type = %q, want %q", got, wantContentType)
	}
}

// conformClaimBusyWhileLeaseHeld verifies that a second concurrent Claim for
// the same (ns, key) while the lease is held returns ClaimBusy.
func conformClaimBusyWhileLeaseHeld(t *testing.T, factory Factory) {
	t.Helper()
	store, _, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()
	state, _, _, err := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("first Claim: state=%v err=%v; want ClaimAcquired nil", state, err)
	}

	state2, rec2, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil {
		t.Fatalf("second Claim: unexpected error: %v", err2)
	}
	if state2 != idempotency.ClaimBusy {
		t.Errorf("second Claim state = %v, want ClaimBusy", state2)
	}
	if rec2 != nil {
		t.Errorf("ClaimBusy: recorded response must be nil, got non-nil")
	}
}

// conformReleaseAllowsReClaim acquires a lease, releases it, then verifies
// that the key can be re-claimed (i.e., the lease is no longer held).
func conformReleaseAllowsReClaim(t *testing.T, factory Factory) {
	t.Helper()
	store, _, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()
	state, _, receipt, err := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("Claim: state=%v err=%v; want ClaimAcquired nil", state, err)
	}

	if err := receipt.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	state2, _, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil {
		t.Fatalf("re-Claim after Release: unexpected error: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("re-Claim state = %v, want ClaimAcquired", state2)
	}
}

// conformLeaseTTLExpiry verifies that after a short lease TTL expires, the
// same (ns, key) can be re-claimed as ClaimAcquired.
func conformLeaseTTLExpiry(t *testing.T, factory Factory) {
	t.Helper()
	store, adv, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()
	state, _, _, err := store.Claim(ctx, conformNS, conformKey, shortLeaseTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("Claim: state=%v err=%v; want ClaimAcquired nil", state, err)
	}

	// Advance past the lease TTL so it expires.
	adv.AdvancePast(shortLeaseTTL)

	state2, _, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil {
		t.Fatalf("re-Claim after TTL expiry: unexpected error: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("re-Claim after TTL expiry: state = %v, want ClaimAcquired (lease should have expired)", state2)
	}
}

// conformStaleTokenRecord verifies the token-guard: acquire lease A, let it
// expire, acquire lease B, then attempt Record on receipt-A — it must fail
// with a non-nil error (stale token).
func conformStaleTokenRecord(t *testing.T, factory Factory) {
	t.Helper()
	store, adv, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()

	// Acquire lease A with a short TTL.
	stateA, _, receiptA, err := store.Claim(ctx, conformNS, conformKey, shortLeaseTTL)
	if err != nil || stateA != idempotency.ClaimAcquired {
		t.Fatalf("Claim (A): state=%v err=%v", stateA, err)
	}

	// Let lease A expire.
	adv.AdvancePast(shortLeaseTTL)

	// Acquire lease B (new owner).
	stateB, _, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil || stateB != idempotency.ClaimAcquired {
		t.Fatalf("Claim (B): state=%v err=%v", stateB, err2)
	}

	// Receipt A (stale token) must fail.
	resp := buildTestResponse(t, 200, []byte(`ok`))
	err3 := receiptA.Record(ctx, &resp, conformDoneTTL)
	if err3 == nil {
		t.Error("stale receipt A Record must return an error, got nil")
	}
}

// conformDifferentNsKeyIndependent verifies that distinct (ns, key) pairs
// do not interfere with each other.
func conformDifferentNsKeyIndependent(t *testing.T, factory Factory) {
	t.Helper()
	store, _, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()

	// Claim under ns1.
	state1, _, _, err1 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err1 != nil || state1 != idempotency.ClaimAcquired {
		t.Fatalf("Claim (ns1): state=%v err=%v", state1, err1)
	}

	// Claim under ns2 (different namespace, same key) must be independent.
	state2, _, _, err2 := store.Claim(ctx, conformAltNS, conformKey, conformLeaseTTL)
	if err2 != nil {
		t.Fatalf("Claim (ns2): unexpected error: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("Claim (ns2) state = %v, want ClaimAcquired (different namespace must not collide)", state2)
	}

	// Claim under ns1 with a different key must also be independent.
	state3, _, _, err3 := store.Claim(ctx, conformNS, conformAltKey, conformLeaseTTL)
	if err3 != nil {
		t.Fatalf("Claim (ns1, altKey): unexpected error: %v", err3)
	}
	if state3 != idempotency.ClaimAcquired {
		t.Errorf("Claim (ns1, altKey) state = %v, want ClaimAcquired (different key must not collide)", state3)
	}
}

// conformDoneTTLExpiry verifies that a recorded response expires after
// doneTTL: Claim → Record with shortDoneTTL → advance past TTL → next Claim
// returns ClaimAcquired (the done record has expired, the key is fresh again).
//
// Uses the TimeAdvancer so no real time.Sleep is required for MemStore; Redis
// backends advance via real sleep in the factory's AdvancePast implementation.
func conformDoneTTLExpiry(t *testing.T, factory Factory) {
	t.Helper()
	store, adv, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()

	// Claim and record with a very short done TTL.
	state, _, receipt, err := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("Claim: state=%v err=%v; want ClaimAcquired nil", state, err)
	}

	resp := buildTestResponse(t, 200, []byte(`{"ok":true}`))
	if err := receipt.Record(ctx, &resp, shortDoneTTL); err != nil {
		t.Fatalf("Record with shortDoneTTL: %v", err)
	}

	// Verify it is in ClaimDone state immediately after Record.
	statePre, recPre, _, errPre := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if errPre != nil {
		t.Fatalf("pre-expiry Claim: unexpected error: %v", errPre)
	}
	if statePre != idempotency.ClaimDone {
		t.Errorf("pre-expiry Claim state = %v, want ClaimDone", statePre)
	}
	if recPre == nil {
		t.Error("pre-expiry Claim: recorded response must be non-nil for ClaimDone")
	}

	// Advance past the done TTL so the done record expires.
	adv.AdvancePast(shortDoneTTL)

	// After expiry the key must be re-claimable as ClaimAcquired.
	statePost, _, _, errPost := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if errPost != nil {
		t.Fatalf("post-expiry Claim: unexpected error: %v", errPost)
	}
	if statePost != idempotency.ClaimAcquired {
		t.Errorf("post-expiry Claim state = %v, want ClaimAcquired (done TTL should have expired)", statePost)
	}
}

// conformStaleTokenRelease verifies that Release on a stale receipt (whose
// lease has expired and been taken over by a new lease holder) does not corrupt
// the new holder's lease. Symmetric to conformStaleTokenRecord.
//
// Sequence: acquire A (shortLeaseTTL) → advance past TTL (A expires) →
// acquire B (long TTL) → A.Release → B should still be ClaimBusy for a third
// Claim (B's lease was not deleted).
func conformStaleTokenRelease(t *testing.T, factory Factory) {
	t.Helper()
	store, adv, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()

	// Acquire lease A with a short TTL.
	stateA, _, receiptA, err := store.Claim(ctx, conformNS, conformKey, shortLeaseTTL)
	if err != nil || stateA != idempotency.ClaimAcquired {
		t.Fatalf("Claim (A): state=%v err=%v", stateA, err)
	}

	// Let lease A expire.
	adv.AdvancePast(shortLeaseTTL)

	// Acquire lease B (new owner, long TTL).
	stateB, _, _, err2 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err2 != nil || stateB != idempotency.ClaimAcquired {
		t.Fatalf("Claim (B): state=%v err=%v", stateB, err2)
	}

	// Stale Release from receipt A: token no longer matches B's lease.
	// Contract: stale Release must be a safe no-op (does not return an error
	// that would cause the caller to crash, and does not delete B's lease).
	_ = receiptA.Release(ctx) // error is tolerated; the key assertion is below.

	// A third Claim must return ClaimBusy because B's lease is still held.
	stateC, _, _, err3 := store.Claim(ctx, conformNS, conformKey, conformLeaseTTL)
	if err3 != nil {
		t.Fatalf("third Claim: unexpected error: %v", err3)
	}
	if stateC != idempotency.ClaimBusy {
		t.Errorf("third Claim state = %v, want ClaimBusy (stale Release must not corrupt B's lease)", stateC)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildTestResponse constructs a minimal RecordedResponse for assertion.
// Uses MarshalRecordedResponse + UnmarshalRecordedResponse (the only
// package-external construction path for RecordedResponse).
func buildTestResponse(t *testing.T, status int, body []byte) idemhttp.RecordedResponse {
	t.Helper()
	// Round-trip through the wire codec: this is the only package-external
	// construction path available (RecordedResponse fields are unexported).
	// We synthesize a minimal JSON blob that UnmarshalRecordedResponse accepts.
	hdr := http.Header{"Content-Type": {"application/json"}}
	raw, err := marshalMinimalResponse(status, body, hdr)
	if err != nil {
		t.Fatalf("buildTestResponse marshal: %v", err)
	}
	resp, err := idemhttp.UnmarshalRecordedResponse(raw)
	if err != nil {
		t.Fatalf("buildTestResponse unmarshal: %v", err)
	}
	return resp
}
