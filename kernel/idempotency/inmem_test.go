package idempotency

import (
	"context"
	"testing"
	"time"
)

func TestInMemClaimer_Claim_AcquiredThenCommitted(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	state, receipt, err := c.Claim(ctx, "k1", time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error on first claim: %v", err)
	}
	if state != ClaimAcquired {
		t.Fatalf("expected ClaimAcquired, got %v", state)
	}
	if err := receipt.Commit(ctx); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	state2, _, err := c.Claim(ctx, "k1", time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("second claim error: %v", err)
	}
	if state2 != ClaimDone {
		t.Fatalf("expected ClaimDone after commit, got %v", state2)
	}
}

func TestInMemClaimer_Claim_BusyWhileInFlight(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	state1, _, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if state1 != ClaimAcquired {
		t.Fatalf("expected first Acquired, got %v", state1)
	}

	state2, _, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if state2 != ClaimBusy {
		t.Fatalf("expected Busy when second claim overlaps, got %v", state2)
	}
}

func TestInMemClaimer_Release_AllowsReclaim(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	_, r, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if err := r.Release(ctx); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	state, _, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if state != ClaimAcquired {
		t.Fatalf("expected Acquired after release, got %v", state)
	}
}

func TestInMemClaimer_LeaseExpiry_AllowsReclaim(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	// Fast-forward clock by mutating `now`.
	base := time.Now()
	c.now = func() time.Time { return base }
	_, _, _ = c.Claim(ctx, "k", time.Second, time.Hour)

	// Advance past lease TTL.
	c.now = func() time.Time { return base.Add(2 * time.Second) }
	state, _, _ := c.Claim(ctx, "k", time.Second, time.Hour)
	if state != ClaimAcquired {
		t.Fatalf("expected reclaim after lease expiry, got %v", state)
	}
}

func TestInMemClaimer_DoubleCommit_Idempotent(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()
	_, r, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("second commit should be idempotent, got %v", err)
	}
}

func TestInMemClaimer_DoubleRelease_Idempotent(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()
	_, r, _ := c.Claim(ctx, "k", time.Minute, time.Hour)
	if err := r.Release(ctx); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := r.Release(ctx); err != nil {
		t.Fatalf("second release should be idempotent, got %v", err)
	}
}
