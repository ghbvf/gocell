// Package sagajournaltest provides a reusable conformance suite for
// implementations of [journal.Journal]. Any implementation — in-memory
// (PR-02) or PostgreSQL-backed (PR-04) — runs RunConformanceSuite to verify
// the full lease-fencing, event-append, and terminal-transition contract.
//
// The suite is a plain .go file (not _test.go) so it can be imported by test
// packages in other layers without being stripped from the build graph.
package sagajournaltest

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Factory constructs a fresh Journal backed by a deterministic clock. The
// returned *FakeClock is the SAME clock the Journal uses internally, so
// tests can advance time to expire leases without sleeping. cleanup is safe
// to call once and must be deferred by the caller.
type Factory func(t *testing.T) (j journal.Journal, clk *clockmock.FakeClock, cleanup func())

// RunConformanceSuite runs the full Journal conformance suite against the
// supplied factory. Every subtest is registered as a t.Run so individual
// cases can be filtered with -run.
func RunConformanceSuite(t *testing.T, factory Factory) {
	t.Helper()

	// Each case maps a subtest name to its conformance function. The leading
	// "category" comments track the spec categories the suite covers. Driving
	// them through a table keeps this registrar short and makes -run filtering
	// trivial.
	cases := []struct {
		name string
		run  func(*testing.T, Factory)
	}{
		// 1: Enqueue + Load basic contract.
		{"Enqueue_ThenLoad_EmptyNonNilSlice", conformEnqueueThenLoad},
		{"Load_NeverEnqueued_KindNotFound", conformLoadNeverEnqueued},
		// 2: Duplicate enqueue.
		{"Enqueue_DuplicateID_KindConflict", conformEnqueueDuplicate},
		// 3: Enqueue validation — non-Pending / non-zero-CurrentStep.
		{"Enqueue_NonPendingInstance_Error", conformEnqueueNonPending},
		{"Enqueue_NonZeroCurrentStep_Error", conformEnqueueNonZeroStep},
		// 4: Append + Load round-trip.
		{"Append_Load_RoundTrip", conformAppendLoadRoundTrip},
		// 5: Version monotonicity.
		{"Append_VersionMonotonicity", conformVersionMonotonicity},
		// 6: Concurrent Append.
		{"Append_Concurrent_DistinctVersions", conformConcurrentAppend},
		// 7: Append payload validation.
		{"Append_InvalidJSONPayload_Rejected", conformAppendInvalidPayload},
		{"Append_ArrayPayload_Rejected", conformAppendArrayPayload},
		{"Append_KindSagaTerminal_Rejected", conformAppendTerminalKindRejected},
		// 8–11: ClaimPending empty / batch cap / second call / contention.
		{"ClaimPending_EmptyStore_NilSliceZeroLeaseID", conformClaimPendingEmpty},
		{"ClaimPending_BatchCap", conformClaimPendingBatchCap},
		{"ClaimPending_SecondCall_DisjointDifferentLeaseID", conformClaimPendingSecondCall},
		{"ClaimPending_Concurrent_NoDuplicate", conformClaimPendingConcurrent},
		// 12: Stale-lease reject on Append.
		{"Append_StaleLease_KindConflict", conformAppendStaleLease},
		// 13–14: Heartbeat fencing + lease extension.
		{"Heartbeat_WrongLease_FalseNil", conformHeartbeatWrongLease},
		{"Heartbeat_CorrectLease_TrueNil", conformHeartbeatCorrectLease},
		{"Heartbeat_ExtendsLease_DoesNotExpire", conformHeartbeatExtendsLease},
		// 16: MarkTerminal happy paths (Pending→Failed, Running→Succeeded,
		// Compensating→Compensated) + illegal-transition rejection.
		{"MarkTerminal_PendingToFailed_TerminalEventAppended_LeaseReleased", conformMarkTerminalHappy},
		{"MarkTerminal_RunningToSucceeded", conformMarkTerminalSucceeded},
		{"MarkTerminal_CompensatingToCompensated", conformCompensationPath},
		{"MarkTerminal_IllegalTransition_Error", conformMarkTerminalIllegalTransition},
		// Append status-projection invariant: compensate-before-start rejected.
		{"Append_CompensateOnPending_Rejected", conformAppendCompensateOnPending},
		// 15: leader handoff — stale leader fenced out while the new one drives on.
		{"LeaderHandoff_StaleLeaderFencedOut", conformLeaderHandoff},
		// 17: MarkTerminal fencing.
		{"MarkTerminal_WrongLease_FalseNil", conformMarkTerminalWrongLease},
		// 18: Terminal instance not re-claimable.
		{"ClaimPending_TerminalInstance_NotReturned", conformTerminalNotReclaimed},
		// 19: RepoReady delegation.
		{"RepoReady", conformRepoReady},
		// Fix E: unknown-instance semantics.
		{"Heartbeat_UnknownInstance_FalseNil", conformHeartbeatUnknownInstance},
		{"MarkTerminal_UnknownInstance_FalseNil", conformMarkTerminalUnknownInstance},
		{"Append_UnknownInstance_KindNotFound", conformAppendUnknownInstance},
		// Fix B: isolated copy of ClaimedInstance.
		{"ClaimedInstance_IsolatedCopy", conformClaimedInstanceIsolatedCopy},
		// Fix F: non-positive batchSize.
		{"ClaimPending_NonPositiveBatchSize_KindInvalid", conformClaimPendingNonPositiveBatchSize},
		// Fix G: step-kind projection paths.
		{"Append_KindStepCompleted_PendingToRunning", conformAppendStepCompletedPendingToRunning},
		{"Append_KindStepFailed_PendingToRunning", conformAppendStepFailedPendingToRunning},
		{"MarkTerminal_RunningToFailed", conformMarkTerminalRunningToFailed},
		{"MarkTerminal_RunningToExpired", conformMarkTerminalRunningToExpired},
		{"MarkTerminal_CompensatingToFailed", conformMarkTerminalCompensatingToFailed},
		{"MarkTerminal_CompensatingToExpired", conformMarkTerminalCompensatingToExpired},
		{"MarkTerminal_CompensatingToSucceeded_KindInvalid", conformMarkTerminalCompensatingToSucceededIllegal},
		{"MarkTerminal_RunningToCompensated_KindInvalid", conformMarkTerminalRunningToCompensatedIllegal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, factory) })
	}
}

// ---------------------------------------------------------------------------
// Exported fixture helpers reused by GREEN-phase tests and PR-04 PG tests.
// ---------------------------------------------------------------------------

// NewInstanceFixture creates a saga.Instance in Pending status using
// saga.NewInstance. id is used as both the instance ID and the definition ID.
// The DefinitionID is set to id+"-def".
func NewInstanceFixture(t *testing.T, id string, now time.Time) saga.Instance {
	t.Helper()
	return saga.NewInstance(idutil.SafeID(id), idutil.SafeID(id+"-def"), now)
}

// NewStepEvent constructs a journal.Event for a step kind. Callers supply the
// kind, step name, and optional payload. Version and CreatedAt are left zero
// (the Journal assigns them on Append).
func NewStepEvent(kind journal.EventKind, step string, payload []byte) journal.Event {
	return journal.Event{
		Kind:     kind,
		StepName: idutil.SafeID(step),
		Payload:  payload,
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// isKindConflict reports whether err carries KindConflict.
func isKindConflict(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindConflict
}

// isKindNotFound reports whether err carries KindNotFound.
func isKindNotFound(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindNotFound
}

// isKindInvalid reports whether err carries KindInvalid.
func isKindInvalid(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindInvalid
}

// mustEnqueue calls Enqueue and fails the test on error.
func mustEnqueue(t *testing.T, j journal.Journal, inst saga.Instance) {
	t.Helper()
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

// mustClaimAll claims up to 100 instances with the standard shortLease and
// returns (claimed slice, batch leaseID), failing the test on error.
func mustClaimAll(t *testing.T, j journal.Journal) ([]journal.ClaimedInstance, idutil.SafeID) {
	t.Helper()
	claimed, leaseID, err := j.ClaimPending(context.Background(), 100, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	return claimed, leaseID
}

// findClaimed returns the ClaimedInstance with the given instance ID, or fails.
func findClaimed(t *testing.T, claimed []journal.ClaimedInstance, id idutil.SafeID) journal.ClaimedInstance {
	t.Helper()
	for _, c := range claimed {
		if c.Instance.ID == id {
			return c
		}
	}
	t.Fatalf("instance %s not found in claimed batch (len=%d)", id, len(claimed))
	return journal.ClaimedInstance{}
}

// Lease durations used across the conformance suite, extracted to package-level
// consts per TEST-TIME-LITERAL-01.
const (
	shortLease    = 10 * time.Second // standard claim lease for fencing tests
	originalLease = 5 * time.Second  // initial lease before a Heartbeat extension
	extendedLease = 60 * time.Second // lease length after a Heartbeat extension
	wellPastLease = shortLease * 10  // advance the clock well past any lease window
)

// appendStep appends a step event under the given lease and fails on error,
// returning the assigned version. The kind parameter controls the event kind so
// callers can exercise different status-projection paths (KindStepStarted,
// KindStepCompleted, KindStepFailed, KindStepCompensated, etc.).
func appendStep(t *testing.T, j journal.Journal, id, leaseID idutil.SafeID, kind journal.EventKind) int64 {
	t.Helper()
	v, err := j.Append(context.Background(), id, leaseID, journal.Event{
		Kind:     kind,
		StepName: "step-one",
	})
	if err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
	return v
}

// loadHasTerminal reports whether the instance's event log contains a
// KindSagaTerminal event.
func loadHasTerminal(t *testing.T, j journal.Journal, id idutil.SafeID) bool {
	t.Helper()
	events, err := j.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range events {
		if e.Kind == journal.KindSagaTerminal {
			return true
		}
	}
	return false
}

// claimOne enqueues a fresh Pending instance and claims it, returning its
// ClaimedInstance. Fails the test if the claim does not return the instance.
func claimOne(t *testing.T, j journal.Journal, clk *clockmock.FakeClock, id string) journal.ClaimedInstance {
	t.Helper()
	inst := NewInstanceFixture(t, id, clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	return findClaimed(t, claimed, inst.ID)
}

// ---------------------------------------------------------------------------
// Category 1
// ---------------------------------------------------------------------------

func conformEnqueueThenLoad(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-enq-load", clk.Now())
	mustEnqueue(t, j, inst)

	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load after Enqueue: %v", err)
	}
	if events == nil {
		t.Error("Load returned nil slice; want non-nil empty slice")
	}
	if len(events) != 0 {
		t.Errorf("Load returned %d events; want 0 (no events appended yet)", len(events))
	}
}

func conformLoadNeverEnqueued(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	_, err := j.Load(context.Background(), "never-enqueued-id")
	if err == nil {
		t.Fatal("Load of never-enqueued instance should return error, got nil")
	}
	if !isKindNotFound(err) {
		t.Errorf("Load of never-enqueued instance: want KindNotFound error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 2
// ---------------------------------------------------------------------------

func conformEnqueueDuplicate(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-dup", clk.Now())
	mustEnqueue(t, j, inst)

	err := j.Enqueue(context.Background(), inst)
	if err == nil {
		t.Fatal("re-Enqueue of same ID should return error, got nil")
	}
	if !isKindConflict(err) {
		t.Errorf("re-Enqueue of same ID: want KindConflict error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 3 — Enqueue validation
// ---------------------------------------------------------------------------

func conformEnqueueNonPending(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	// Build a bad instance by constructing one and mutating its Status.
	// ValidateNew will be called by Enqueue.
	inst := NewInstanceFixture(t, "inst-non-pending", clk.Now())
	inst.Status = saga.StatusRunning // break the invariant

	err := j.Enqueue(context.Background(), inst)
	if err == nil {
		t.Fatal("Enqueue of non-Pending instance should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Enqueue of non-Pending instance: want KindInvalid error, got %v", err)
	}
}

func conformEnqueueNonZeroStep(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-nonzero-step", clk.Now())
	inst.CurrentStep = 1 // break the invariant

	err := j.Enqueue(context.Background(), inst)
	if err == nil {
		t.Fatal("Enqueue of instance with CurrentStep!=0 should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Enqueue of non-zero CurrentStep: want KindInvalid error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 4 — Append + Load round-trip
// ---------------------------------------------------------------------------

func conformAppendLoadRoundTrip(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-append-rt", clk.Now())
	mustEnqueue(t, j, inst)

	claimed, leaseID := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	wantPayload := []byte(`{"step":"one"}`)
	appendTime := clk.Now()
	version, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  wantPayload,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if version != 1 {
		t.Errorf("Append returned version=%d; want 1", version)
	}

	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Load returned %d events; want 1", len(events))
	}
	e := events[0]
	if e.Version != 1 {
		t.Errorf("event Version=%d; want 1", e.Version)
	}
	if e.Kind != journal.KindStepStarted {
		t.Errorf("event Kind=%v; want KindStepStarted", e.Kind)
	}
	if e.StepName != "step-one" {
		t.Errorf("event StepName=%q; want %q", e.StepName, "step-one")
	}
	if !bytes.Equal(e.Payload, wantPayload) {
		t.Errorf("event Payload=%q; want %q", e.Payload, wantPayload)
	}
	// CreatedAt must be stamped by the Journal. With the deterministic FakeClock
	// (no Advance between claim and append) it equals appendTime exactly; for a
	// PG-backed store the DB stamps now() independently, so we assert the weaker
	// "non-zero and not before appendTime" that holds for both.
	if e.CreatedAt.IsZero() {
		t.Error("event CreatedAt must be stamped by the Journal, got zero")
	}
	if e.CreatedAt.Before(appendTime) {
		t.Errorf("event CreatedAt=%v is before append time %v", e.CreatedAt, appendTime)
	}
	if leaseID == "" {
		t.Error("leaseID must be non-empty")
	}
}

// ---------------------------------------------------------------------------
// Category 5 — Version monotonicity
// ---------------------------------------------------------------------------

func conformVersionMonotonicity(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const n = 5
	inst := NewInstanceFixture(t, "inst-monotonic", clk.Now())
	mustEnqueue(t, j, inst)

	claimed, _ := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	for i := range n {
		v, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
			Kind:     journal.KindStepStarted,
			StepName: idutil.SafeID("step"),
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		want := int64(i + 1)
		if v != want {
			t.Errorf("Append %d: got version %d; want %d", i, v, want)
		}
	}

	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != n {
		t.Fatalf("Load returned %d events; want %d", len(events), n)
	}
	for i, e := range events {
		want := int64(i + 1)
		if e.Version != want {
			t.Errorf("events[%d].Version=%d; want %d", i, e.Version, want)
		}
	}
	// Verify ascending order.
	for i := 1; i < len(events); i++ {
		if events[i].Version <= events[i-1].Version {
			t.Errorf("events not in ascending version order at index %d: %d <= %d",
				i, events[i].Version, events[i-1].Version)
		}
	}
}

// ---------------------------------------------------------------------------
// Category 6 — Concurrent Append
// ---------------------------------------------------------------------------

func conformConcurrentAppend(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const G = 8
	inst := NewInstanceFixture(t, "inst-concurrent-append", clk.Now())
	mustEnqueue(t, j, inst)

	claimed, _ := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	versions := make([]int64, G)
	var wg sync.WaitGroup
	for i := range G {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
				Kind:     journal.KindStepStarted,
				StepName: idutil.SafeID("step"),
			})
			if err != nil {
				t.Errorf("goroutine %d Append: %v", idx, err)
				return
			}
			versions[idx] = v
		}(i)
	}
	wg.Wait()

	// All versions must be distinct (each goroutine claimed a unique version).
	seen := make(map[int64]bool, G)
	for _, v := range versions {
		if v == 0 {
			continue // goroutine errored above
		}
		if seen[v] {
			t.Errorf("duplicate version %d in concurrent Append results", v)
		}
		seen[v] = true
	}

	// Load and verify event count.
	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != G {
		t.Errorf("Load returned %d events; want %d", len(events), G)
	}

	// Versions must be contiguous 1..G.
	sortedVersions := make([]int64, len(events))
	for i, e := range events {
		sortedVersions[i] = e.Version
	}
	sort.Slice(sortedVersions, func(i, k int) bool { return sortedVersions[i] < sortedVersions[k] })
	for i, v := range sortedVersions {
		want := int64(i + 1)
		if v != want {
			t.Errorf("sorted version[%d]=%d; want %d", i, v, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Category 7 — Append payload validation
// ---------------------------------------------------------------------------

func conformAppendInvalidPayload(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-bad-json", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _ := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  []byte(`not-valid-json`),
	})
	if err == nil {
		t.Fatal("Append with invalid JSON payload should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append with invalid JSON payload: want KindInvalid error, got %v", err)
	}
}

func conformAppendArrayPayload(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-array-payload", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _ := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  []byte(`[1,2,3]`),
	})
	if err == nil {
		t.Fatal("Append with array JSON payload should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append with array JSON payload: want KindInvalid error, got %v", err)
	}
}

func conformAppendTerminalKindRejected(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-terminal-kind", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _ := mustClaimAll(t, j)
	ci := findClaimed(t, claimed, inst.ID)

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindSagaTerminal,
		// KindSagaTerminal is non-step kind, so no StepName required.
	})
	if err == nil {
		t.Fatal("Append with KindSagaTerminal should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append with KindSagaTerminal: want KindInvalid error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 8 — ClaimPending on empty store
// ---------------------------------------------------------------------------

func conformClaimPendingEmpty(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	claimed, leaseID, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending on empty store: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimPending on empty store: got %d claimed; want 0", len(claimed))
	}
	if leaseID != "" {
		t.Errorf("ClaimPending on empty store: got non-empty leaseID %q; want empty", leaseID)
	}
}

// ---------------------------------------------------------------------------
// Category 9 — ClaimPending batch cap
// ---------------------------------------------------------------------------

func conformClaimPendingBatchCap(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	now := clk.Now()
	for i := range 3 {
		id := idutil.SafeID("inst-batch-" + string(rune('a'+i)))
		mustEnqueue(t, j, saga.NewInstance(id, id+"-def", now))
	}

	claimed, leaseID, err := j.ClaimPending(context.Background(), 2, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending(2): %v", err)
	}
	if len(claimed) != 2 {
		t.Errorf("ClaimPending(2): got %d; want 2", len(claimed))
	}
	if leaseID == "" {
		t.Error("ClaimPending(2): leaseID must be non-empty")
	}
	// All claimed instances share the same batch leaseID.
	for _, ci := range claimed {
		if ci.LeaseID != leaseID {
			t.Errorf("ClaimedInstance.LeaseID=%q != batch leaseID %q", ci.LeaseID, leaseID)
		}
	}
}

// ---------------------------------------------------------------------------
// Category 10 — ClaimPending second call (disjoint + different leaseID)
// ---------------------------------------------------------------------------

func conformClaimPendingSecondCall(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	now := clk.Now()
	for i := range 3 {
		id := idutil.SafeID("inst-second-" + string(rune('a'+i)))
		mustEnqueue(t, j, saga.NewInstance(id, id+"-def", now))
	}

	first, leaseA, err := j.ClaimPending(context.Background(), 2, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("ClaimPending first: got %d; want 2", len(first))
	}

	second, leaseB, err := j.ClaimPending(context.Background(), 2, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending second: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("ClaimPending second: got %d; want 1", len(second))
	}

	// LeaseIDs must differ.
	if leaseA == leaseB {
		t.Errorf("first and second batch leaseIDs must differ; both are %q", leaseA)
	}

	// Claimed sets must be disjoint.
	firstIDs := make(map[idutil.SafeID]bool, len(first))
	for _, ci := range first {
		firstIDs[ci.Instance.ID] = true
	}
	for _, ci := range second {
		if firstIDs[ci.Instance.ID] {
			t.Errorf("instance %s appeared in both claim batches", ci.Instance.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Category 11 — ClaimPending contention
// ---------------------------------------------------------------------------

func conformClaimPendingConcurrent(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const total = 10
	now := clk.Now()
	for i := range total {
		id := idutil.SafeID("inst-conc-" + string(rune('a'+i)))
		mustEnqueue(t, j, saga.NewInstance(id, id+"-def", now))
	}

	const G = 5
	type result struct {
		claimed []journal.ClaimedInstance
	}
	resultsCh := make(chan result, G)

	var wg sync.WaitGroup
	for range G {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, _, err := j.ClaimPending(context.Background(), total, shortLease)
			if err != nil {
				t.Errorf("ClaimPending concurrent: %v", err)
				resultsCh <- result{}
				return
			}
			resultsCh <- result{claimed: claimed}
		}()
	}
	wg.Wait()
	close(resultsCh)

	seen := make(map[idutil.SafeID]bool)
	for r := range resultsCh {
		for _, ci := range r.claimed {
			if seen[ci.Instance.ID] {
				t.Errorf("instance %s claimed twice in concurrent ClaimPending", ci.Instance.ID)
			}
			seen[ci.Instance.ID] = true
		}
	}
}

// ---------------------------------------------------------------------------
// Category 12 — Stale-lease reject on Append
// ---------------------------------------------------------------------------

func conformAppendStaleLease(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-stale-append", clk.Now())
	mustEnqueue(t, j, inst)

	// Worker A claims.
	claimedA, leaseA, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedA) == 0 {
		t.Fatalf("ClaimPending (worker A): err=%v len=%d", err, len(claimedA))
	}
	ciA := findClaimed(t, claimedA, inst.ID)
	_ = ciA

	// Advance clock past lease expiry so A's lease expires.
	clk.Advance(shortLease + time.Second)

	// Worker B claims the recovered instance with a new lease.
	claimedB, leaseB, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedB) == 0 {
		t.Fatalf("ClaimPending (worker B): err=%v len=%d", err, len(claimedB))
	}
	if leaseA == leaseB {
		t.Fatalf("worker A and B leases must differ; both are %q", leaseA)
	}

	// Worker A's stale Append must return KindConflict.
	_, err = j.Append(context.Background(), inst.ID, leaseA, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
	})
	if err == nil {
		t.Fatal("stale Append should return error, got nil")
	}
	if !isKindConflict(err) {
		t.Errorf("stale Append: want KindConflict error, got %v", err)
	}

	// Worker B's valid Append must succeed.
	ciB := findClaimed(t, claimedB, inst.ID)
	_, err = j.Append(context.Background(), ciB.Instance.ID, ciB.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
	})
	if err != nil {
		t.Fatalf("fresh Append with worker B lease: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 13 — Heartbeat fencing
// ---------------------------------------------------------------------------

func conformHeartbeatWrongLease(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-hb-wrong", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	// Call Heartbeat with a wrong (fabricated) leaseID.
	ok, err := j.Heartbeat(context.Background(), inst.ID, "wrong-lease-id", shortLease)
	if err != nil {
		t.Fatalf("Heartbeat with wrong lease should not error, got: %v", err)
	}
	if ok {
		t.Error("Heartbeat with wrong lease should return ok=false, got ok=true")
	}
}

func conformHeartbeatCorrectLease(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-hb-correct", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	ok, err := j.Heartbeat(context.Background(), inst.ID, ci.LeaseID, shortLease)
	if err != nil {
		t.Fatalf("Heartbeat with correct lease: %v", err)
	}
	if !ok {
		t.Error("Heartbeat with correct lease should return ok=true, got ok=false")
	}
}

// ---------------------------------------------------------------------------
// Category 14 — Heartbeat extends lease
// ---------------------------------------------------------------------------

func conformHeartbeatExtendsLease(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-hb-extend", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, originalLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	// Heartbeat with a much longer lease duration.
	ok, err := j.Heartbeat(context.Background(), inst.ID, ci.LeaseID, extendedLease)
	if err != nil {
		t.Fatalf("Heartbeat extend: %v", err)
	}
	if !ok {
		t.Fatal("Heartbeat extend: expected ok=true")
	}

	// Advance clock past the ORIGINAL expiry but within the new extended one.
	clk.Advance(originalLease + time.Second)

	// ClaimPending must NOT re-lease this instance (it's covered by the extended lease).
	newClaimed, _, err := j.ClaimPending(context.Background(), 10, originalLease)
	if err != nil {
		t.Fatalf("ClaimPending after heartbeat extend: %v", err)
	}
	for _, c := range newClaimed {
		if c.Instance.ID == inst.ID {
			t.Error("instance should not be re-claimable after heartbeat extended its lease")
		}
	}
}

// ---------------------------------------------------------------------------
// Category 16 — MarkTerminal happy path + transition validation
//
// The Journal advances the non-terminal projection status as a function of the
// appended event kind (see Journal.Append godoc): the first forward step event
// moves Pending→Running, and a StepCompensated event moves Running→Compensating.
// MarkTerminal then commits a terminal status legal from the current one. These
// tests exercise all three reachable happy paths:
//   - Pending → Failed     (no step events; saga abandoned before starting)
//   - Running → Succeeded  (after a forward step event)
//   - Compensating → Compensated (after a forward step + a compensate event)
// ---------------------------------------------------------------------------

func conformMarkTerminalHappy(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-mark-terminal")

	// Transition: Pending → Failed (legal per statusTransitions, no step events).
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal(Pending→Failed): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Pending→Failed): expected ok=true")
	}

	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal did not append a KindSagaTerminal event")
	}

	// Lease released + terminal: a later ClaimPending (after any lease window
	// expires) must not return the now-terminal instance.
	clk.Advance(shortLease + time.Second)
	nextClaimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending after MarkTerminal: %v", err)
	}
	for _, c := range nextClaimed {
		if c.Instance.ID == ci.Instance.ID {
			t.Error("terminal instance must not be returned by ClaimPending")
		}
	}
}

// conformMarkTerminalSucceeded drives Pending→Running via a forward step event,
// then commits Running→Succeeded.
func conformMarkTerminalSucceeded(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-succeeded")

	// Forward step event moves Pending → Running.
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusSucceeded)
	if err != nil {
		t.Fatalf("MarkTerminal(Running→Succeeded): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Running→Succeeded): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Succeeded) did not append a KindSagaTerminal event")
	}
}

// conformCompensationPath drives Pending→Running→Compensating via step events,
// then commits Compensating→Compensated.
func conformCompensationPath(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensated")

	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)     // Pending → Running
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompensated) // Running → Compensating

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusCompensated)
	if err != nil {
		t.Fatalf("MarkTerminal(Compensating→Compensated): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Compensating→Compensated): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Compensated) did not append a KindSagaTerminal event")
	}
}

// conformAppendCompensateOnPending asserts the status-projection invariant that
// a StepCompensated event on a still-Pending instance is rejected (a step
// cannot be compensated before the saga has started — Pending↛Compensating).
func conformAppendCompensateOnPending(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensate-on-pending")

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepCompensated,
		StepName: "step-one",
	})
	if err == nil {
		t.Fatal("Append(StepCompensated) on a Pending instance should be rejected, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append(StepCompensated) on Pending: want KindInvalid error, got %v", err)
	}
}

// conformLeaderHandoff is the full category-15 scenario: leader A claims and
// heartbeats, then stops; the lease expires; leader B reclaims with a fresh
// lease; A's subsequent Heartbeat and Append are fenced out while B drives the
// instance to a terminal state.
func conformLeaderHandoff(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-handoff", clk.Now())
	mustEnqueue(t, j, inst)

	// Leader A claims and heartbeats.
	claimedA, leaseA, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedA) == 0 {
		t.Fatalf("ClaimPending (A): err=%v len=%d", err, len(claimedA))
	}
	if okHB, errHB := j.Heartbeat(context.Background(), inst.ID, leaseA, shortLease); errHB != nil || !okHB {
		t.Fatalf("Heartbeat (A): ok=%v err=%v", okHB, errHB)
	}

	// A stops; its lease expires.
	clk.Advance(shortLease + time.Second)

	// Leader B reclaims the same instance with a fresh, different lease.
	claimedB, leaseB, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedB) == 0 {
		t.Fatalf("ClaimPending (B): err=%v len=%d", err, len(claimedB))
	}
	if leaseA == leaseB {
		t.Fatalf("handoff: B's lease must differ from A's; both are %q", leaseA)
	}
	findClaimed(t, claimedB, inst.ID) // B must own it now

	// A's stale Heartbeat is fenced out (ok=false, no error).
	if okHB, errHB := j.Heartbeat(context.Background(), inst.ID, leaseA, shortLease); errHB != nil || okHB {
		t.Errorf("stale leader A Heartbeat: want (false,nil), got (%v,%v)", okHB, errHB)
	}
	// A's stale Append is rejected with a conflict.
	if _, errAp := j.Append(context.Background(), inst.ID, leaseA, journal.Event{
		Kind: journal.KindStepStarted, StepName: "step-one",
	}); errAp == nil || !isKindConflict(errAp) {
		t.Errorf("stale leader A Append: want KindConflict, got %v", errAp)
	}

	// B drives the instance forward and to a terminal state.
	appendStep(t, j, inst.ID, leaseB, journal.KindStepStarted) // Pending → Running
	if okMT, errMT := j.MarkTerminal(context.Background(), inst.ID, leaseB, saga.StatusSucceeded); errMT != nil || !okMT {
		t.Fatalf("leader B MarkTerminal(Succeeded): ok=%v err=%v", okMT, errMT)
	}
}

func conformMarkTerminalIllegalTransition(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-bad-terminal", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	// Pending → Succeeded is NOT a legal transition
	// (statusTransitions: Pending → {Running, Failed, Expired}).
	_, err = j.MarkTerminal(context.Background(), inst.ID, ci.LeaseID, saga.StatusSucceeded)
	if err == nil {
		t.Fatal("MarkTerminal(Pending→Succeeded) should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(Pending→Succeeded): want KindInvalid error, got %v", err)
	}

	// Pending → Compensated is also illegal.
	_, err = j.MarkTerminal(context.Background(), inst.ID, ci.LeaseID, saga.StatusCompensated)
	if err == nil {
		t.Fatal("MarkTerminal(Pending→Compensated) should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(Pending→Compensated): want KindInvalid error, got %v", err)
	}

	// StatusRunning is not terminal at all.
	_, err = j.MarkTerminal(context.Background(), inst.ID, ci.LeaseID, saga.StatusRunning)
	if err == nil {
		t.Fatal("MarkTerminal with non-terminal finalStatus should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(non-terminal): want KindInvalid error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category 17 — MarkTerminal fencing
// ---------------------------------------------------------------------------

func conformMarkTerminalWrongLease(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-mt-fence", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	// Call MarkTerminal with the wrong leaseID.
	ok, err := j.MarkTerminal(context.Background(), inst.ID, "wrong-lease-id", saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal with wrong lease should not error, got: %v", err)
	}
	if ok {
		t.Error("MarkTerminal with wrong lease should return ok=false, got ok=true")
	}

	// Instance must still be claimable (status unchanged).
	// Advance clock to let the original lease expire so we can reclaim.
	clk.Advance(shortLease + time.Second)
	reClaimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending after wrong-lease MarkTerminal: %v", err)
	}
	var found bool
	for _, c := range reClaimed {
		if c.Instance.ID == inst.ID {
			found = true
		}
	}
	if !found {
		t.Error("instance should still be claimable after wrong-lease MarkTerminal (status unchanged)")
	}
}

// ---------------------------------------------------------------------------
// Category 15 (leader handoff scenario)
// ---------------------------------------------------------------------------
// Note: this is primarily documented as category 15 in the spec, but it is
// closely related to the MarkTerminal tests. The test verifies that after
// A's lease expires and B claims, A's later operations are rejected while B
// can drive to terminal.

// Category 18 is covered by conformMarkTerminalHappy (terminal not reclaimed
// after MarkTerminal succeeds).

// ---------------------------------------------------------------------------
// Category 18 — Terminal instance not re-claimable (dedicated test)
// ---------------------------------------------------------------------------

func conformTerminalNotReclaimed(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-no-reclaim", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	ok, err := j.MarkTerminal(context.Background(), inst.ID, ci.LeaseID, saga.StatusExpired)
	if err != nil {
		t.Fatalf("MarkTerminal(Expired): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Expired): expected ok=true")
	}

	// Advance clock so any lease window is well past.
	clk.Advance(wellPastLease)

	for range 3 {
		c2, _, err2 := j.ClaimPending(context.Background(), 10, shortLease)
		if err2 != nil {
			t.Fatalf("ClaimPending after terminal: %v", err2)
		}
		for _, c := range c2 {
			if c.Instance.ID == inst.ID {
				t.Error("terminal instance must never be returned by ClaimPending")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Category 19 — RepoReady
// ---------------------------------------------------------------------------

func conformRepoReady(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()
	prober, ok := j.(healthz.RepoProber)
	if !ok {
		t.Fatal("Journal must implement healthz.RepoProber")
	}
	t.Run("healthy", func(t *testing.T) {
		if err := prober.RepoReady(context.Background()); err != nil {
			t.Fatalf("RepoReady on healthy journal = %v, want nil", err)
		}
	})
	// In-memory implementations have no differentiated failure domain; the PR-04
	// PG store will extend this with a schema-broken case. Recorded as a skipped
	// subtest for coverage visibility. This mirrors the shape of
	// celltest.RunRepoReadinessConformance WITHOUT importing it (CELLTEST-B
	// forbids kernel/ importing kernel/cell/celltest).
	t.Run("schema-broken", func(t *testing.T) {
		t.Skip("in-memory implementation has no differentiated failure domain")
	})
}

// ---------------------------------------------------------------------------
// Fix E — unknown-instance semantics
// ---------------------------------------------------------------------------

// conformHeartbeatUnknownInstance verifies that Heartbeat on a never-enqueued
// instance returns (false, nil) — callers cannot distinguish this from a stale
// lease (by interface contract).
func conformHeartbeatUnknownInstance(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	ok, err := j.Heartbeat(context.Background(), "never-enqueued-id", "any-lease", shortLease)
	if err != nil {
		t.Fatalf("Heartbeat on unknown instance should return nil error, got: %v", err)
	}
	if ok {
		t.Error("Heartbeat on unknown instance should return ok=false, got ok=true")
	}
}

// conformMarkTerminalUnknownInstance verifies that MarkTerminal on a never-enqueued
// instance returns (false, nil).
func conformMarkTerminalUnknownInstance(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	ok, err := j.MarkTerminal(context.Background(), "never-enqueued-id", "any-lease", saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal on unknown instance should return nil error, got: %v", err)
	}
	if ok {
		t.Error("MarkTerminal on unknown instance should return ok=false, got ok=true")
	}
}

// conformAppendUnknownInstance verifies that Append on a never-enqueued instance
// returns a KindNotFound error.
func conformAppendUnknownInstance(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	_, err := j.Append(context.Background(), "never-enqueued-id", "any-lease", journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
	})
	if err == nil {
		t.Fatal("Append on unknown instance should return error, got nil")
	}
	if !isKindNotFound(err) {
		t.Errorf("Append on unknown instance: want KindNotFound error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fix B — isolated copy of ClaimedInstance
// ---------------------------------------------------------------------------

// conformClaimedInstanceIsolatedCopy verifies that the *time.Time pointer fields
// returned in a ClaimedInstance are deep-copied (not shared with journal
// internals). Writing through a returned pointer must not affect subsequent
// claim results.
func conformClaimedInstanceIsolatedCopy(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-isolated-copy")

	// The UpdatedAt pointer in the claimed instance should be nil for a freshly
	// Pending instance (no updates yet); but CompletedAt is definitely nil.
	// To exercise cloneInstance we advance the clock and do an Append to get a
	// non-nil UpdatedAt, then reclaim.
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)

	// Let the first lease expire and reclaim so we get a fresh ClaimedInstance
	// with a potentially non-nil UpdatedAt (after projection advance).
	clk.Advance(shortLease + time.Second)
	claimed2, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil {
		t.Fatalf("re-ClaimPending: %v", err)
	}
	ci2 := findClaimed(t, claimed2, ci.Instance.ID)

	// If UpdatedAt is non-nil, write through it and verify a second claim still
	// gets a different pointer (isolated copy).
	if ci2.Instance.UpdatedAt != nil {
		poison := time.Unix(0, 1)
		*ci2.Instance.UpdatedAt = poison // mutate returned copy

		// Reclaim (after expiry) and check UpdatedAt is not poisoned.
		clk.Advance(shortLease + time.Second)
		claimed3, _, err := j.ClaimPending(context.Background(), 10, shortLease)
		if err != nil {
			t.Fatalf("re-ClaimPending after poison: %v", err)
		}
		ci3 := findClaimed(t, claimed3, ci.Instance.ID)
		if ci3.Instance.UpdatedAt != nil && ci3.Instance.UpdatedAt.Equal(poison) {
			t.Error("ClaimedInstance.UpdatedAt pointer was shared with journal state (not deep-copied)")
		}
		// Pointers must be distinct objects.
		if ci2.Instance.UpdatedAt == ci3.Instance.UpdatedAt {
			t.Error("ClaimedInstance.UpdatedAt from two different claims must not share the same pointer")
		}
	}
}

// ---------------------------------------------------------------------------
// Fix F — non-positive batchSize
// ---------------------------------------------------------------------------

// conformClaimPendingNonPositiveBatchSize verifies that batchSize 0 and -1 both
// return a KindInvalid error.
func conformClaimPendingNonPositiveBatchSize(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	for _, batchSize := range []int{0, -1} {
		_, _, err := j.ClaimPending(context.Background(), batchSize, shortLease)
		if err == nil {
			t.Fatalf("ClaimPending(batchSize=%d) should return error, got nil", batchSize)
		}
		if !isKindInvalid(err) {
			t.Errorf("ClaimPending(batchSize=%d): want KindInvalid error, got %v", batchSize, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Fix G — step-kind projection + terminal-transition coverage
// ---------------------------------------------------------------------------

// conformAppendStepCompletedPendingToRunning verifies that KindStepCompleted on
// a Pending instance advances the projection to Running (same as KindStepStarted).
func conformAppendStepCompletedPendingToRunning(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-completed-pending-running")

	// KindStepCompleted on Pending should advance to Running (non-compensated forward step).
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompleted)

	// Now MarkTerminal(Succeeded) should work from Running.
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusSucceeded)
	if err != nil {
		t.Fatalf("MarkTerminal(Running→Succeeded) after KindStepCompleted: %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Running→Succeeded) after KindStepCompleted: expected ok=true")
	}
}

// conformAppendStepFailedPendingToRunning verifies that KindStepFailed on a
// Pending instance advances the projection to Running.
func conformAppendStepFailedPendingToRunning(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-failed-pending-running")

	// KindStepFailed on Pending should advance to Running.
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepFailed)

	// MarkTerminal(Failed) from Running is legal.
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal(Running→Failed) after KindStepFailed: %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Running→Failed) after KindStepFailed: expected ok=true")
	}
}

// conformMarkTerminalRunningToFailed verifies Running → Failed is a valid terminal path.
func conformMarkTerminalRunningToFailed(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-running-failed")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal(Running→Failed): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Running→Failed): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Running→Failed) did not append a KindSagaTerminal event")
	}
}

// conformMarkTerminalRunningToExpired verifies Running → Expired is a valid terminal path.
func conformMarkTerminalRunningToExpired(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-running-expired")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusExpired)
	if err != nil {
		t.Fatalf("MarkTerminal(Running→Expired): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Running→Expired): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Running→Expired) did not append a KindSagaTerminal event")
	}
}

// conformMarkTerminalCompensatingToFailed verifies Compensating → Failed is a valid terminal path.
func conformMarkTerminalCompensatingToFailed(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensating-failed")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)     // Pending → Running
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompensated) // Running → Compensating

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	if err != nil {
		t.Fatalf("MarkTerminal(Compensating→Failed): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Compensating→Failed): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Compensating→Failed) did not append a KindSagaTerminal event")
	}
}

// conformMarkTerminalCompensatingToExpired verifies Compensating → Expired is a valid terminal path.
func conformMarkTerminalCompensatingToExpired(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensating-expired")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)     // Pending → Running
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompensated) // Running → Compensating

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusExpired)
	if err != nil {
		t.Fatalf("MarkTerminal(Compensating→Expired): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Compensating→Expired): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Compensating→Expired) did not append a KindSagaTerminal event")
	}
}

// conformMarkTerminalCompensatingToSucceededIllegal verifies that
// Compensating → Succeeded is an illegal transition (returns KindInvalid).
func conformMarkTerminalCompensatingToSucceededIllegal(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensating-succeeded-illegal")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)     // Pending → Running
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompensated) // Running → Compensating

	_, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusSucceeded)
	if err == nil {
		t.Fatal("MarkTerminal(Compensating→Succeeded) should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(Compensating→Succeeded): want KindInvalid error, got %v", err)
	}
}

// conformMarkTerminalRunningToCompensatedIllegal verifies that
// Running → Compensated is an illegal transition (returns KindInvalid).
func conformMarkTerminalRunningToCompensatedIllegal(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-running-compensated-illegal")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running

	_, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusCompensated)
	if err == nil {
		t.Fatal("MarkTerminal(Running→Compensated) should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(Running→Compensated): want KindInvalid error, got %v", err)
	}
}
