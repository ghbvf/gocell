package journal_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
)

// epoch is the fixed start time used across MemJournal tests.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newMemFactory returns a sagajournaltest.Factory backed by a MemJournal and a
// deterministic FakeClock. The SAME clock instance is passed to NewMemJournal
// and returned to the suite, so tests can advance time to expire leases.
func newMemFactory(t *testing.T) (journal.Journal, *clockmock.FakeClock, func()) {
	t.Helper()
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	return j, clk, func() {}
}

// TestMemJournal_Conformance runs the full conformance suite against MemJournal.
func TestMemJournal_Conformance(t *testing.T) {
	sagajournaltest.RunConformanceSuite(t, newMemFactory)
}

// ---------------------------------------------------------------------------
// White-box / coverage-extension tests
// ---------------------------------------------------------------------------

// TestNewMemJournal_NilClock verifies that passing a nil Clock panics.
func TestNewMemJournal_NilClock(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewMemJournal(nil) did not panic; expected clock guard panic")
		}
	}()
	_, _ = journal.NewMemJournal(nil)
}

// TestLoad_DefensiveCopy verifies that mutating a Payload returned by Load
// does not affect the journal's internal copy.
func TestLoad_DefensiveCopy(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	inst := sagajournaltest.NewInstanceFixture(t, "inst-copy-test", clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, _, err := j.ClaimPending(context.Background(), 10, 30*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := claimed[0]

	wantPayload := []byte(`{"key":"value"}`)
	_, err = j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  wantPayload,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Load the events and mutate the returned payload.
	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	events[0].Payload[0] = 'X' // mutate the returned copy

	// Re-load: the journal's copy must be unchanged.
	events2, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load (second): %v", err)
	}
	if events2[0].Payload[0] == 'X' {
		t.Error("Load returned a reference to internal payload; expected a defensive copy")
	}
	if !bytes.Equal(events2[0].Payload, wantPayload) {
		t.Errorf("stored payload corrupted: got %q; want %q", events2[0].Payload, wantPayload)
	}
}

// TestClaimPending_DefensiveCopy verifies that mutating a returned Instance
// does not affect journal state. Specifically, a re-claim after lease expiry
// should return the original unmodified Status.
func TestClaimPending_DefensiveCopy(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	inst := sagajournaltest.NewInstanceFixture(t, "inst-claim-copy", clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	const leaseDuration = 5 * time.Second
	claimed, _, err := j.ClaimPending(context.Background(), 10, leaseDuration)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	// Mutate the returned ClaimedInstance.Instance; the journal must not see this.
	claimed[0].Instance.CurrentStep = 999

	// Let the lease expire and reclaim.
	clk.Advance(leaseDuration + time.Second)
	reClaimed, _, err := j.ClaimPending(context.Background(), 10, leaseDuration)
	if err != nil || len(reClaimed) == 0 {
		t.Fatalf("ClaimPending (reclaim): err=%v len=%d", err, len(reClaimed))
	}

	// The re-claimed instance should have the original CurrentStep (0), not 999.
	if reClaimed[0].Instance.CurrentStep == 999 {
		t.Error("ClaimPending returned a reference; mutation leaked into journal state")
	}
}

// TestAppend_NilPayload verifies that a nil Payload is accepted (treated as empty).
func TestAppend_NilPayload(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	inst := sagajournaltest.NewInstanceFixture(t, "inst-nil-payload", clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, _, err := j.ClaimPending(context.Background(), 10, 30*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := claimed[0]

	v, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  nil,
	})
	if err != nil {
		t.Fatalf("Append with nil payload: %v", err)
	}
	if v != 1 {
		t.Errorf("expected version 1, got %d", v)
	}
}

// TestAppend_NeverClaimed verifies that Append on an enqueued but unclaimed
// instance (leaseID = "") returns a stale-lease KindConflict error.
func TestAppend_NeverClaimed(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	inst := sagajournaltest.NewInstanceFixture(t, "inst-unclaimed", clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	_, err = j.Append(context.Background(), inst.ID, "some-lease-id", journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
	})
	if err == nil {
		t.Fatal("Append without a claim should return error, got nil")
	}
}

// TestHeartbeat_UnknownInstance verifies that Heartbeat on an unknown instance
// returns (false, nil) rather than an error.
func TestHeartbeat_UnknownInstance(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	ok, err := j.Heartbeat(context.Background(), "does-not-exist", "any-lease", 30*time.Second)
	if err != nil {
		t.Fatalf("Heartbeat on unknown instance returned error: %v", err)
	}
	if ok {
		t.Error("Heartbeat on unknown instance should return ok=false")
	}
}

// TestMarkTerminal_UnknownInstance verifies that MarkTerminal on an unknown
// instance returns (false, nil).
func TestMarkTerminal_UnknownInstance(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	ok, err := j.MarkTerminal(context.Background(), "does-not-exist", "any-lease", 0)
	if err != nil {
		t.Fatalf("MarkTerminal on unknown instance returned error: %v", err)
	}
	if ok {
		t.Error("MarkTerminal on unknown instance should return ok=false")
	}
}

// TestRepoReady verifies that MemJournal.RepoReady always returns nil.
func TestRepoReady(t *testing.T) {
	clk := clockmock.New(epoch)
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	if err := j.RepoReady(context.Background()); err != nil {
		t.Fatalf("RepoReady: %v", err)
	}
}
