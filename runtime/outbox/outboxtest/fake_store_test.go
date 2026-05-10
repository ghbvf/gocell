package outboxtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// fakeStoreClaimTTL is the claimTTL passed to ReclaimStale in the reclaim test.
const fakeStoreClaimTTL = testtime.D60s

// fakeStoreBaseDelay is the baseDelay passed to ReclaimStale.
const fakeStoreBaseDelay = testtime.D5s

// fakeStoreMaxDelay is the maxDelay passed to ReclaimStale.
const fakeStoreMaxDelay = testtime.D5min

// TestFakeStore_ConformanceSuite runs the full Store conformance suite against
// FakeStore to verify that the in-memory implementation is spec-compliant.
func TestFakeStore_ConformanceSuite(t *testing.T) {
	factory := func(t *testing.T, seed []outbox.ClaimedEntry) outbox.Store {
		t.Helper()
		s := outboxtest.NewFakeStore()
		s.Seed(seed...)
		return s
	}
	outboxtest.RunStoreConformanceSuite(t, factory)
}

// TestFakeStore_SeedSnapshot verifies the Seed / Snapshot round-trip.
func TestFakeStore_SeedSnapshot(t *testing.T) {
	s := outboxtest.NewFakeStore()

	entries := []outbox.ClaimedEntry{
		{
			Entry: kout.Entry{
				ID:        "row-a",
				EventType: "ev.a",
				Topic:     "ev.a",
				Payload:   []byte(`{"k":"v"}`),
				CreatedAt: time.Now(),
			},
			Attempts: 1,
		},
		{
			Entry: kout.Entry{
				ID:        "row-b",
				EventType: "ev.b",
				Topic:     "ev.b",
				Payload:   []byte(`{"k":"w"}`),
				CreatedAt: time.Now(),
			},
			Attempts: 0,
		},
	}
	s.Seed(entries...)

	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 rows in snapshot, got %d", len(snap))
	}

	// Snapshot is sorted by ID.
	if snap[0].Entry.ID != "row-a" {
		t.Errorf("expected first row ID=row-a, got %s", snap[0].Entry.ID)
	}
	if snap[1].Entry.ID != "row-b" {
		t.Errorf("expected second row ID=row-b, got %s", snap[1].Entry.ID)
	}

	// Default status is pending.
	for _, row := range snap {
		if row.Status != "pending" {
			t.Errorf("row %s: expected status=pending, got %s", row.Entry.ID, row.Status)
		}
	}
}

// TestFakeStore_WithClock verifies that WithClock injection affects time-dependent methods.
func TestFakeStore_WithClock(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	s := outboxtest.NewFakeStore()
	s.WithClock(func() time.Time { return base })

	s.Seed(outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        "clk-1",
			EventType: "ev.clk",
			Topic:     "ev.clk",
			Payload:   []byte(`{"x":1}`),
			CreatedAt: base,
		},
	})

	ctx := context.Background()
	_, _ = s.ClaimPending(ctx, 10)

	// Snapshot should show claimedAt = base.
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 row, got %d", len(snap))
	}
	if snap[0].ClaimedAt == nil {
		t.Fatal("expected claimedAt to be set after ClaimPending")
	}
	if !snap[0].ClaimedAt.Equal(base) {
		t.Errorf("claimedAt: got %v, want %v", *snap[0].ClaimedAt, base)
	}

	// Advance clock and reclaim; nextRetryAt should use new clock value.
	advanced := base.Add(testtime.D2min)
	s.WithClock(func() time.Time { return advanced })

	count, err := s.ReclaimStale(ctx, fakeStoreClaimTTL, 5, fakeStoreBaseDelay, fakeStoreMaxDelay, 1000)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 reclaimed, got %d", count)
	}

	snap = s.Snapshot()
	if snap[0].Status != "pending" {
		t.Errorf("expected status=pending after reclaim, got %s", snap[0].Status)
	}
	if snap[0].NextRetryAt == nil {
		t.Error("expected nextRetryAt to be set after reclaim")
	} else if snap[0].NextRetryAt.Before(advanced) {
		t.Errorf("nextRetryAt %v should be after advanced clock %v", *snap[0].NextRetryAt, advanced)
	}
}

// TestFakeStore_SeedOverwrite verifies that Seed overwrites existing rows.
func TestFakeStore_SeedOverwrite(t *testing.T) {
	s := outboxtest.NewFakeStore()

	s.Seed(outbox.ClaimedEntry{
		Entry:    kout.Entry{ID: "ow-1", EventType: "ev", Topic: "ev", Payload: []byte(`{}`), CreatedAt: time.Now()},
		Attempts: 0,
	})
	s.Seed(outbox.ClaimedEntry{
		Entry:    kout.Entry{ID: "ow-1", EventType: "ev", Topic: "ev", Payload: []byte(`{"overwrite":true}`), CreatedAt: time.Now()},
		Attempts: 3,
	})

	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 row after overwrite, got %d", len(snap))
	}
	if snap[0].Attempts != 3 {
		t.Errorf("Attempts: got %d, want 3", snap[0].Attempts)
	}
}

// TestFakeStore_WaitFor_ImmediateSatisfaction verifies that WaitFor returns
// nil immediately when cond is already true on the initial snapshot, without
// blocking on any state change.
func TestFakeStore_WaitFor_ImmediateSatisfaction(t *testing.T) {
	s := outboxtest.NewFakeStore()
	s.Seed(outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID: "wf-immediate", EventType: "ev", Topic: "ev",
			Payload: []byte(`{}`), CreatedAt: time.Now(),
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()

	start := time.Now()
	err := s.WaitFor(ctx, func(snap []outboxtest.FakeRow) bool {
		return len(snap) == 1 && snap[0].Entry.ID == "wf-immediate"
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitFor returned err = %v, want nil", err)
	}
	if elapsed > testtime.D50ms {
		t.Errorf("WaitFor took %v, expected near-immediate return (no polling)", elapsed)
	}
}

// TestFakeStore_WaitFor_WakesOnMutation verifies that WaitFor blocks while
// cond is false and wakes up immediately when a state-mutating method
// (MarkPublished here) is called from another goroutine.
func TestFakeStore_WaitFor_WakesOnMutation(t *testing.T) {
	s := outboxtest.NewFakeStore()
	s.Seed(outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID: "wf-wake", EventType: "ev", Topic: "ev",
			Payload: []byte(`{}`), CreatedAt: time.Now(),
		},
	})

	// Claim so MarkPublished can transition claiming → published.
	claimed, err := s.ClaimPending(context.Background(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending failed: %v, len=%d", err, len(claimed))
	}
	leaseID := claimed[0].LeaseID

	// Trigger publish in another goroutine after a short delay so we can
	// observe that WaitFor was actually blocked (not polling).
	go func() {
		time.Sleep(testtime.D20ms)
		_, _ = s.MarkPublished(context.Background(), "wf-wake", leaseID)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()

	start := time.Now()
	err = s.WaitFor(ctx, func(snap []outboxtest.FakeRow) bool {
		return len(snap) == 1 && snap[0].Status == "published"
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitFor returned err = %v, want nil", err)
	}
	if elapsed < testtime.D20ms {
		t.Errorf("WaitFor returned in %v, expected to block until mutation (>= 20ms)", elapsed)
	}
	if elapsed > testtime.D200ms {
		t.Errorf("WaitFor took %v, expected to wake within tens of ms after mutation", elapsed)
	}
}

// TestFakeStore_WaitFor_CtxCancelled verifies that WaitFor returns ctx.Err()
// when ctx is canceled while cond is still false (no false positives).
func TestFakeStore_WaitFor_CtxCancelled(t *testing.T) {
	s := outboxtest.NewFakeStore()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D50ms)
	defer cancel()

	err := s.WaitFor(ctx, func(snap []outboxtest.FakeRow) bool {
		return len(snap) > 0 // unsatisfiable: store is empty and never seeded
	})
	if err == nil {
		t.Fatal("WaitFor returned nil, want ctx deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitFor err = %v, want context.DeadlineExceeded", err)
	}
}
