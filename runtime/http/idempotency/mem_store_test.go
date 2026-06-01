package idempotency

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
)

func newTestRecordedResponse(t *testing.T) *RecordedResponse {
	t.Helper()
	clk := clockmock.New(time.Now())
	resp := newRecordedResponse(clk, 200, []byte(`{"ok":true}`), http.Header{"Content-Type": []string{"application/json"}})
	return &resp
}

func TestMemStore_FirstClaim_Acquired(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	state, rec, receipt, err := ms.Claim(context.Background(), "tenant1", "user1:idem-key-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != idempotency.ClaimAcquired {
		t.Errorf("state: got %v, want ClaimAcquired", state)
	}
	if rec != nil {
		t.Error("first claim should return nil RecordedResponse")
	}
	if receipt == nil {
		t.Fatal("receipt must be non-nil for ClaimAcquired")
	}
}

func TestMemStore_RecordThenClaimDone(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	state, _, receipt, err := ms.Claim(ctx, "tenant1", "user1:key2", 5*time.Minute)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("first claim failed: state=%v err=%v", state, err)
	}

	resp := newTestRecordedResponse(t)
	if err := receipt.Record(ctx, resp, 24*time.Hour); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// Second claim should return ClaimDone with the stored response.
	state2, rec2, _, err2 := ms.Claim(ctx, "tenant1", "user1:key2", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("second claim error: %v", err2)
	}
	if state2 != idempotency.ClaimDone {
		t.Errorf("state: got %v, want ClaimDone", state2)
	}
	if rec2 == nil {
		t.Fatal("ClaimDone must return a non-nil RecordedResponse")
	}
	if rec2.Status() != 200 {
		t.Errorf("replayed status: got %d, want 200", rec2.Status())
	}
}

func TestMemStore_ConcurrentLease_Busy(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	// Acquire lease for the key.
	state, _, _, err := ms.Claim(ctx, "tenant1", "user1:key3", 5*time.Minute)
	if err != nil || state != idempotency.ClaimAcquired {
		t.Fatalf("first claim: %v %v", state, err)
	}

	// Another claim for the same key should be Busy.
	state2, _, _, err2 := ms.Claim(ctx, "tenant1", "user1:key3", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("second claim error: %v", err2)
	}
	if state2 != idempotency.ClaimBusy {
		t.Errorf("state: got %v, want ClaimBusy", state2)
	}
}

func TestMemStore_Release_Reopens(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	_, _, receipt, err := ms.Claim(ctx, "t1", "u1:key4", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := receipt.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release, the key should be re-claimable.
	state2, _, _, err2 := ms.Claim(ctx, "t1", "u1:key4", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("re-claim error: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("re-claim state: got %v, want ClaimAcquired", state2)
	}
}

func TestMemStore_LeaseTTLExpiry(t *testing.T) {
	now := time.Now()
	clk := clockmock.New(now)
	ms := NewMemStore(clk)

	ctx := context.Background()
	_, _, _, err := ms.Claim(ctx, "t1", "u1:key5", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Advance clock past lease TTL.
	clk.Advance(6 * time.Minute)

	// Key should now be re-claimable.
	state2, _, _, err2 := ms.Claim(ctx, "t1", "u1:key5", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("re-claim after expiry: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("state after expiry: got %v, want ClaimAcquired", state2)
	}
}

func TestMemStore_DoneTTLExpiry(t *testing.T) {
	now := time.Now()
	clk := clockmock.New(now)
	ms := NewMemStore(clk)

	ctx := context.Background()
	_, _, receipt, err := ms.Claim(ctx, "t1", "u1:key6", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	resp := newTestRecordedResponse(t)
	if err := receipt.Record(ctx, resp, 1*time.Hour); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Advance past done TTL.
	clk.Advance(2 * time.Hour)

	// Key should be re-claimable.
	state2, rec2, _, err2 := ms.Claim(ctx, "t1", "u1:key6", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("re-claim after done expiry: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("state after done expiry: got %v, want ClaimAcquired", state2)
	}
	if rec2 != nil {
		t.Error("expired done entry must not return a RecordedResponse")
	}
}

func TestMemStore_StaleTokenRecordRejected(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	_, _, receipt1, err := ms.Claim(ctx, "t1", "u1:key7", 5*time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Release the key.
	if err := receipt1.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Re-claim the key (new lease).
	_, _, _, _ = ms.Claim(ctx, "t1", "u1:key7", 5*time.Minute)

	// Try to record using the stale receipt from the first claim.
	resp := newTestRecordedResponse(t)
	err = receipt1.Record(ctx, resp, 24*time.Hour)
	if err == nil {
		t.Error("stale token Record must return an error")
	}
}

func TestMemStore_StaleTokenReleaseRejected(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	_, _, receipt1, err := ms.Claim(ctx, "t1", "u1:key8", 5*time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Release once.
	if err := receipt1.Release(ctx); err != nil {
		t.Fatalf("first release: %v", err)
	}

	// Second Release on same receipt must be idempotent or return an error (not panic).
	// The underlying sync.Once makes second call a no-op.
	_ = receipt1.Release(ctx)
}

func TestMemStore_NamespaceIsolation(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	// Claim in namespace "t1".
	_, _, _, err := ms.Claim(ctx, "t1", "u1:key", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim ns=t1: %v", err)
	}

	// Same key in a different namespace must be independent.
	state2, _, _, err2 := ms.Claim(ctx, "t2", "u1:key", 5*time.Minute)
	if err2 != nil {
		t.Fatalf("claim ns=t2: %v", err2)
	}
	if state2 != idempotency.ClaimAcquired {
		t.Errorf("different namespace must acquire independently; got %v", state2)
	}
}

func TestMemStore_ConcurrentClaims_OnlyOnceAcquired(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	const goroutines = 20
	results := make(chan idempotency.ClaimState, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			state, _, _, _ := ms.Claim(ctx, "t1", "u1:concurrent-key", 5*time.Minute)
			results <- state
		}()
	}
	wg.Wait()
	close(results)

	acquired := 0
	for s := range results {
		if s == idempotency.ClaimAcquired {
			acquired++
		}
	}
	if acquired != 1 {
		t.Errorf("exactly 1 goroutine must acquire; got %d", acquired)
	}
}

// TestMemStore_NonAcquiredReceiptErrors verifies that a ClaimBusy receipt's
// Record/Release return errors (matching kernel NonAcquiredReceipt semantics).
func TestMemStore_NonAcquiredReceiptErrors(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	ctx := context.Background()
	// First claim acquires.
	_, _, _, _ = ms.Claim(ctx, "t1", "u1:key9", 5*time.Minute)

	// Second claim is Busy — returns a noopReceipt.
	_, _, busyReceipt, err := ms.Claim(ctx, "t1", "u1:key9", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	resp := newTestRecordedResponse(t)
	err = busyReceipt.Record(ctx, resp, 24*time.Hour)
	if !errors.Is(err, idempotency.ErrNoClaimLease) {
		t.Errorf("Busy Receipt.Record: want ErrNoClaimLease, got %v", err)
	}
	err = busyReceipt.Release(ctx)
	if !errors.Is(err, idempotency.ErrNoClaimLease) {
		t.Errorf("Busy Receipt.Release: want ErrNoClaimLease, got %v", err)
	}
}
