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

// Package-level string constants extracted per SonarQube duplication rules.
const (
	stepOne          = "step-one"
	anyLease         = "any-lease"
	neverEnqueuedID  = "never-enqueued-id"
	fmtLoadErr       = "Load: %v"
	fmtClaimErr      = "ClaimPending: err=%v len=%d"
	fmtAppendCompErr = "Append(KindCompensationStarted): %v"
)

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
		{"Append_KindSagaSucceeded_Rejected", conformAppendTerminalKindRejected},
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
		// 16: MarkTerminal illegal-transition rejection. (Terminal happy paths are
		// driven from the terminalHappyPaths registry via runTerminalCoverage —
		// SAGA-STATUS-FANOUT-COVERAGE-01 — not from this table.)
		{"MarkTerminal_IllegalTransition_Error", conformMarkTerminalIllegalTransition},
		// Append status-projection invariant: StepCompensated outside Compensating rejected.
		{"Append_StepCompensatedOutsideCompensating_Rejected", conformStepCompensatedOutsideCompensating},
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
		// C6 (#1210): Compensating → Failed is rejected; the valid terminal is
		// CompensationFailed (covered by runTerminalCoverage).
		{"MarkTerminal_CompensatingToFailed_Rejected", conformMarkTerminalCompensatingToFailedRejected},
		{"MarkTerminal_CompensatingToExpired", conformMarkTerminalCompensatingToExpired},
		{"MarkTerminal_CompensatingToSucceeded_KindInvalid", conformMarkTerminalCompensatingToSucceededIllegal},
		{"MarkTerminal_RunningToCompensated_KindInvalid", conformMarkTerminalRunningToCompensatedIllegal},
		// NEW: terminal-event replay, phase-rejection, leader-handoff F2, non-positive lease.
		{"TerminalEvent_ReplayableStatus", conformTerminalEventReplayableStatus},
		{"Append_CompensationStarted_OnPending_Rejected", conformAppendCompensationStartedOnPendingRejected},
		{"Append_ForwardStep_WhileCompensating_Rejected", conformAppendForwardStepWhileCompensatingRejected},
		{"LeaderHandoff_ReadsCompensatingPhase", conformLeaderHandoffReadsCompensatingPhase},
		{"ClaimPending_NonPositiveLeaseDuration_KindInvalid", conformClaimPendingNonPositiveLeaseDuration},
		{"Heartbeat_NonPositiveLeaseDuration_KindInvalid", conformHeartbeatNonPositiveLeaseDuration},
		// F6 (#1210): KindStepCompensationFailed phase legality.
		{"Append_StepCompensationFailedLegalDuringCompensating", conformStepCompensationFailedLegalDuringCompensating},
		{"Append_StepCompensationFailedOutsideCompensating_Rejected", conformStepCompensationFailedOutsideCompensating},
		{"MarkTerminal_CompensatingToCompensationFailed_TerminalEventAppended", conformSagaCompensationFailedTerminal},
		// PR-04 review carry-over: lease exact-equality boundary (memjournal
		// fenced uses !Before(now) → valid at ==; PG SQL must align).
		{"Append_ExactlyAtLeaseExpiry_StillValid", conformAppendExactlyAtLeaseExpiryStillValid},
		{"ClaimPending_ExactlyAtLeaseExpiry_NotEligible", conformClaimPendingExactlyAtLeaseExpiryNotEligible},
		{"Heartbeat_ExactlyAtLeaseExpiry_StillExtends", conformHeartbeatExactlyAtLeaseExpiryStillExtends},
		// PR-04 review carry-over: Append error precedence — instance-existence
		// and lease fence take precedence over event-shape validation.
		{"Append_UnknownInstanceWithBadPayload_PrefersNotFound", conformAppendUnknownWithBadPayloadPrefersNotFound},
		// PR-04 review carry-over: MaxPayloadBytes contract.
		{"Append_PayloadOverMaxBytes_KindInvalid", conformAppendPayloadOverMaxBytes},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, factory) })
	}

	// Terminal happy-path coverage is driven from the terminalHappyPaths registry
	// (a compile-time exhaustiveness gate; see terminal_coverage_gen.go) rather
	// than ad-hoc table rows, so a newly-added terminal saga.Status cannot ship
	// without a happy-path driver. SAGA-STATUS-FANOUT-COVERAGE-01.
	runTerminalCoverage(t, factory)
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

// requireCode fails the test when err does not unwrap to *errcode.Error with
// the expected Code. Used alongside isKindXxx to lock the dedicated saga
// sentinels (ErrSagaNotFound / ErrSagaStaleLease / ErrSagaDuplicateInstance)
// per PR-04 (#959) — both memjournal and PGJournal must emit the same Code so
// operator routing is independent of storage backend.
func requireCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error does not unwrap to *errcode.Error: %v", err)
	}
	if ec.Code != want {
		t.Fatalf("error Code=%q, want %q (err=%v)", ec.Code, want, err)
	}
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
		StepName: stepOne,
	})
	if err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
	return v
}

// loadHasTerminal reports whether the instance's event log contains any
// terminal event kind (as determined by EventKind.IsTerminal).
func loadHasTerminal(t *testing.T, j journal.Journal, id idutil.SafeID) bool {
	t.Helper()
	events, err := j.Load(context.Background(), id)
	if err != nil {
		t.Fatalf(fmtLoadErr, err)
	}
	for _, e := range events {
		if e.Kind.IsTerminal() {
			return true
		}
	}
	return false
}

// loadTerminalKind returns the terminal EventKind from the instance's event log,
// or fails if none is found.
func loadTerminalKind(t *testing.T, j journal.Journal, id idutil.SafeID) journal.EventKind {
	t.Helper()
	events, err := j.Load(context.Background(), id)
	if err != nil {
		t.Fatalf(fmtLoadErr, err)
	}
	for _, e := range events {
		if e.Kind.IsTerminal() {
			return e.Kind
		}
	}
	t.Fatalf("no terminal event found in log for instance %s", id)
	return 0
}

// claimOne enqueues a fresh Pending instance and claims it, returning its
// ClaimedInstance. Fails the test if the claim does not return the instance.
func claimOne(t *testing.T, j journal.Journal, clk *clockmock.FakeClock, id string) journal.ClaimedInstance {
	t.Helper()
	inst := NewInstanceFixture(t, id, clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimed))
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

	_, err := j.Load(context.Background(), neverEnqueuedID)
	if err == nil {
		t.Fatal("Load of never-enqueued instance should return error, got nil")
	}
	if !isKindNotFound(err) {
		t.Errorf("Load of never-enqueued instance: want KindNotFound error, got %v", err)
	}
	requireCode(t, err, errcode.ErrSagaNotFound)
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
	requireCode(t, err, errcode.ErrSagaDuplicateInstance)
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
		StepName: stepOne,
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
		t.Fatalf(fmtLoadErr, err)
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
	if e.StepName != stepOne {
		t.Errorf("event StepName=%q; want %q", e.StepName, stepOne)
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
		t.Fatalf(fmtLoadErr, err)
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
		t.Fatalf(fmtLoadErr, err)
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
		StepName: stepOne,
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
		StepName: stepOne,
		Payload:  []byte(`[1,2,3]`),
	})
	if err == nil {
		t.Fatal("Append with array JSON payload should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append with array JSON payload: want KindInvalid error, got %v", err)
	}
}

// conformAppendTerminalKindRejected asserts that Append with any of the 5
// terminal kinds is rejected — terminal events are written only via MarkTerminal.
func conformAppendTerminalKindRejected(t *testing.T, factory Factory) {
	t.Helper()

	terminalKinds := []journal.EventKind{
		journal.KindSagaSucceeded,
		journal.KindSagaFailed,
		journal.KindSagaCompensated,
		journal.KindSagaExpired,
		journal.KindSagaCompensationFailed,
	}
	for _, kind := range terminalKinds {
		kind := kind
		t.Run(kind.String(), func(t *testing.T) {
			t.Helper()
			j, clk, cleanup := factory(t)
			defer cleanup()

			inst := NewInstanceFixture(t, "inst-terminal-kind-"+kind.String(), clk.Now())
			mustEnqueue(t, j, inst)
			claimed, _ := mustClaimAll(t, j)
			ci := findClaimed(t, claimed, inst.ID)

			_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
				Kind: kind,
			})
			if err == nil {
				t.Fatalf("Append with %s should return error, got nil", kind)
			}
			if !isKindInvalid(err) {
				t.Errorf("Append with %s: want KindInvalid error, got %v", kind, err)
			}
		})
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
		t.Fatalf(fmtClaimErr, err, len(claimedA))
	}
	ciA := findClaimed(t, claimedA, inst.ID)
	_ = ciA

	// Advance clock past lease expiry so A's lease expires.
	clk.Advance(shortLease + time.Second)

	// Worker B claims the recovered instance with a new lease.
	claimedB, leaseB, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedB) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimedB))
	}
	if leaseA == leaseB {
		t.Fatalf("worker A and B leases must differ; both are %q", leaseA)
	}

	// Worker A's stale Append must return KindConflict.
	_, err = j.Append(context.Background(), inst.ID, leaseA, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("stale Append should return error, got nil")
	}
	if !isKindConflict(err) {
		t.Errorf("stale Append: want KindConflict error, got %v", err)
	}
	requireCode(t, err, errcode.ErrSagaStaleLease)

	// Worker B's valid Append must succeed.
	ciB := findClaimed(t, claimedB, inst.ID)
	_, err = j.Append(context.Background(), ciB.Instance.ID, ciB.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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
// Terminal happy-path coverage — SAGA-STATUS-FANOUT-COVERAGE-01
//
// terminalHappyPaths registers, per TERMINAL saga.Status, the driver that drives
// a saga to that terminal via a SUCCESSFUL MarkTerminal. It is a KEYLESS literal
// of terminalCoverage (terminal_coverage_gen.go), so adding a terminal status
// const → `gocell generate saga-coverage` adds a struct field → this literal
// fails to compile ("too few values in struct literal") until a driver is
// supplied. That is the compile-time exhaustiveness gate. runTerminalCoverage
// runs each driver and the HARNESS — not the driver — asserts the happy-path
// outcome, so a negative or mis-mapped driver is caught at runtime (got != want
// || !ok). Illegal-transition / fencing MarkTerminal cases stay in the cases
// table; only happy paths belong here.
//
// The legal source phase per terminal derives from statusTransitions, not from
// the const, so the drivers are hand-written (not codegen):
//   - Pending → Failed                  (no step events)
//   - Running → Succeeded               (after a forward step event)
//   - Compensating → Compensated        (StepStarted + CompensationStarted + StepCompensated)
//   - Running → Expired                 (after a forward step event)
//   - Compensating → CompensationFailed (rollback itself failed)
// ---------------------------------------------------------------------------

// terminalDriver drives a freshly-claimed saga to a terminal MarkTerminal call
// and returns the instance ID, the status it targeted, and the call outcome.
// terminalCoverage (terminal_coverage_gen.go) is the field-per-terminal struct
// whose keyless literal below is the exhaustiveness gate.
type terminalDriver func(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error)

//go:generate gocell generate saga-coverage
var terminalHappyPaths = terminalCoverage{
	driveSucceeded,
	driveFailed,
	driveCompensated,
	driveExpired,
	driveCompensationFailed,
}

// runTerminalCoverage drives every terminal happy path and asserts the uniform
// success contract: MarkTerminal returns (true, nil), the driver reached the
// status its registry slot promises, the log carries the matching terminal event
// kind, and the lease is released (the terminal instance is not re-claimable).
func runTerminalCoverage(t *testing.T, factory Factory) {
	t.Helper()
	drivers := terminalHappyPaths.drivers()
	for i, drv := range drivers {
		want := terminalCoverageWant[i]
		t.Run("TerminalHappyPath_"+want.String(), func(t *testing.T) {
			assertTerminalHappyPath(t, factory, drv, want)
		})
	}
}

// assertTerminalHappyPath runs one driver and asserts the uniform happy-path
// contract: MarkTerminal succeeds, the driver reached the status its registry
// slot promises, the log carries the matching terminal event kind, and the lease
// is released.
func assertTerminalHappyPath(t *testing.T, factory Factory, drv terminalDriver, want saga.Status) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	instID, got, ok, err := drv(t, j, clk)
	if err != nil {
		t.Fatalf("MarkTerminal(→%v): unexpected error: %v", want, err)
	}
	if !ok {
		t.Fatalf("MarkTerminal(→%v): expected ok=true", want)
	}
	if got != want {
		t.Fatalf("driver returned got=%v, want %v (terminalHappyPaths slot mis-mapped)", got, want)
	}
	gotKind := loadTerminalKind(t, j, instID)
	wantKind, okKind := journal.TerminalEventKind(want)
	if !okKind || gotKind != wantKind {
		t.Fatalf("terminal event kind=%v, want %v (journal.TerminalEventKind(%v) ok=%v)", gotKind, wantKind, want, okKind)
	}
	assertTerminalLeaseReleased(t, j, clk, instID)
}

// assertTerminalLeaseReleased asserts a terminal instance is not re-claimable
// after its lease window expires.
func assertTerminalLeaseReleased(t *testing.T, j journal.Journal, clk *clockmock.FakeClock, instID idutil.SafeID) {
	t.Helper()
	clk.Advance(wellPastLease)
	nextClaimed, _, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil {
		t.Fatalf("ClaimPending after terminal: %v", err)
	}
	for _, c := range nextClaimed {
		if c.Instance.ID == instID {
			t.Error("terminal instance must not be returned by ClaimPending after its lease expires")
		}
	}
}

// driveFailed drives Pending → Failed (no step events; saga abandoned before
// starting).
func driveFailed(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error) {
	t.Helper()
	ci := claimOne(t, j, clk, "inst-mark-terminal")
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	return ci.Instance.ID, saga.StatusFailed, ok, err
}

// driveSucceeded drives Running → Succeeded (after a forward step event).
func driveSucceeded(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error) {
	t.Helper()
	ci := claimOne(t, j, clk, "inst-succeeded")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusSucceeded)
	return ci.Instance.ID, saga.StatusSucceeded, ok, err
}

// driveCompensated drives Compensating → Compensated.
func driveCompensated(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error) {
	t.Helper()
	ci := claimOne(t, j, clk, "inst-compensated")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	if _, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindCompensationStarted, // Running → Compensating (no StepName needed)
	}); err != nil {
		t.Fatalf(fmtAppendCompErr, err)
	}
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepCompensated) // legal while Compensating
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusCompensated)
	return ci.Instance.ID, saga.StatusCompensated, ok, err
}

// driveExpired drives Running → Expired (after a forward step event).
func driveExpired(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error) {
	t.Helper()
	ci := claimOne(t, j, clk, "inst-running-expired")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusExpired)
	return ci.Instance.ID, saga.StatusExpired, ok, err
}

// driveCompensationFailed drives Compensating → CompensationFailed (the rollback
// phase itself encountered a step failure).
func driveCompensationFailed(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) (idutil.SafeID, saga.Status, bool, error) {
	t.Helper()
	ci := driveToCompensating(t, j, clk, "inst-compensating-failed")
	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusCompensationFailed)
	return ci.Instance.ID, saga.StatusCompensationFailed, ok, err
}

// conformStepCompensatedOutsideCompensating asserts that KindStepCompensated
// is rejected unless the saga is already in the Compensating phase.
func conformStepCompensatedOutsideCompensating(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	// Case 1: Pending — StepCompensated must be rejected.
	ci := claimOne(t, j, clk, "inst-compensate-outside-compensating-pending")
	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepCompensated,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("Append(StepCompensated) on a Pending instance should be rejected, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append(StepCompensated) on Pending: want KindInvalid error, got %v", err)
	}

	// Case 2: Running — StepCompensated must also be rejected.
	ci2 := claimOne(t, j, clk, "inst-compensate-outside-compensating-running")
	appendStep(t, j, ci2.Instance.ID, ci2.LeaseID, journal.KindStepStarted) // Pending → Running
	_, err = j.Append(context.Background(), ci2.Instance.ID, ci2.LeaseID, journal.Event{
		Kind:     journal.KindStepCompensated,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("Append(StepCompensated) on a Running instance should be rejected, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append(StepCompensated) on Running: want KindInvalid error, got %v", err)
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
		t.Fatalf(fmtClaimErr, err, len(claimedA))
	}
	if okHB, errHB := j.Heartbeat(context.Background(), inst.ID, leaseA, shortLease); errHB != nil || !okHB {
		t.Fatalf("Heartbeat (A): ok=%v err=%v", okHB, errHB)
	}

	// A stops; its lease expires.
	clk.Advance(shortLease + time.Second)

	// Leader B reclaims the same instance with a fresh, different lease.
	claimedB, leaseB, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedB) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimedB))
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
		Kind: journal.KindStepStarted, StepName: stepOne,
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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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

// Category 18 is also covered by runTerminalCoverage (every terminal driver
// asserts the instance is not reclaimed after MarkTerminal succeeds); the
// dedicated test below pins the property in isolation.

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
		t.Fatalf(fmtClaimErr, err, len(claimed))
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

	ok, err := j.Heartbeat(context.Background(), neverEnqueuedID, anyLease, shortLease)
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

	ok, err := j.MarkTerminal(context.Background(), neverEnqueuedID, anyLease, saga.StatusFailed)
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

	_, err := j.Append(context.Background(), neverEnqueuedID, anyLease, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("Append on unknown instance should return error, got nil")
	}
	if !isKindNotFound(err) {
		t.Errorf("Append on unknown instance: want KindNotFound error, got %v", err)
	}
	requireCode(t, err, errcode.ErrSagaNotFound)
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
		t.Error("MarkTerminal(Running→Failed) did not append a terminal event")
	}
}

// driveToCompensating is a shared setup helper that drives a newly-claimed
// instance from Pending through Running to Compensating, returning the
// ClaimedInstance. Deduplicates the setup portion shared by the
// Compensating→{Failed,Expired} terminal paths and related tests.
func driveToCompensating(t *testing.T, j journal.Journal, clk *clockmock.FakeClock, id string) journal.ClaimedInstance {
	t.Helper()
	ci := claimOne(t, j, clk, id)
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindCompensationStarted, // Running → Compensating
	})
	if err != nil {
		t.Fatalf(fmtAppendCompErr, err)
	}
	return ci
}

// conformMarkTerminalCompensatingToFailedRejected verifies that
// Compensating → Failed is no longer a valid transition after C6 (#1210).
// The correct terminal for a rollback failure is StatusCompensationFailed.
func conformMarkTerminalCompensatingToFailedRejected(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := driveToCompensating(t, j, clk, "inst-compensating-to-failed-rejected")

	_, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusFailed)
	if err == nil {
		t.Fatal("MarkTerminal(Compensating→Failed) should return error after C6 split, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("MarkTerminal(Compensating→Failed): want KindInvalid error, got %v", err)
	}
}

// conformMarkTerminalCompensatingToExpired verifies Compensating → Expired is a valid terminal path.
// Must reach Compensating first via StepStarted + CompensationStarted.
func conformMarkTerminalCompensatingToExpired(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := driveToCompensating(t, j, clk, "inst-compensating-expired")

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusExpired)
	if err != nil {
		t.Fatalf("MarkTerminal(Compensating→Expired): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Compensating→Expired): expected ok=true")
	}
	if !loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("MarkTerminal(Compensating→Expired) did not append a terminal event")
	}
}

// conformMarkTerminalCompensatingToSucceededIllegal verifies that
// Compensating → Succeeded is an illegal transition (returns KindInvalid).
func conformMarkTerminalCompensatingToSucceededIllegal(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensating-succeeded-illegal")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindCompensationStarted, // Running → Compensating
	})
	if err != nil {
		t.Fatalf(fmtAppendCompErr, err)
	}

	_, err = j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusSucceeded)
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

// ---------------------------------------------------------------------------
// NEW: TerminalEvent_ReplayableStatus
// ---------------------------------------------------------------------------

// conformTerminalEventReplayableStatus drives two instances to distinct terminal
// states and verifies that the event log alone encodes which terminal was reached
// (Load replays the correct distinct terminal kind).
func conformTerminalEventReplayableStatus(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	// Instance A: Pending → Failed (no step events).
	ciA := claimOne(t, j, clk, "inst-replay-failed")
	ok, err := j.MarkTerminal(context.Background(), ciA.Instance.ID, ciA.LeaseID, saga.StatusFailed)
	if err != nil || !ok {
		t.Fatalf("MarkTerminal(A, Failed): ok=%v err=%v", ok, err)
	}

	// Instance B: Pending → Running → Succeeded.
	ciB := claimOne(t, j, clk, "inst-replay-succeeded")
	appendStep(t, j, ciB.Instance.ID, ciB.LeaseID, journal.KindStepStarted)
	ok, err = j.MarkTerminal(context.Background(), ciB.Instance.ID, ciB.LeaseID, saga.StatusSucceeded)
	if err != nil || !ok {
		t.Fatalf("MarkTerminal(B, Succeeded): ok=%v err=%v", ok, err)
	}

	// Load each and assert the distinct terminal kind is present.
	kindA := loadTerminalKind(t, j, ciA.Instance.ID)
	if kindA != journal.KindSagaFailed {
		t.Errorf("instance A: want terminal kind KindSagaFailed, got %s", kindA)
	}
	kindB := loadTerminalKind(t, j, ciB.Instance.ID)
	if kindB != journal.KindSagaSucceeded {
		t.Errorf("instance B: want terminal kind KindSagaSucceeded, got %s", kindB)
	}
}

// ---------------------------------------------------------------------------
// NEW: Append_CompensationStarted_OnPending_Rejected
// ---------------------------------------------------------------------------

// conformAppendCompensationStartedOnPendingRejected asserts that
// KindCompensationStarted is rejected on a Pending instance
// (Running→Compensating is the only legal transition).
func conformAppendCompensationStartedOnPendingRejected(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-compensation-started-on-pending")

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindCompensationStarted,
	})
	if err == nil {
		t.Fatal("Append(KindCompensationStarted) on a Pending instance should be rejected, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append(KindCompensationStarted) on Pending: want KindInvalid error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// NEW: Append_ForwardStep_WhileCompensating_Rejected
// ---------------------------------------------------------------------------

// conformAppendForwardStepWhileCompensatingRejected asserts that forward step
// events (KindStepStarted) are rejected once the saga is in the Compensating
// phase.
func conformAppendForwardStepWhileCompensatingRejected(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-forward-step-while-compensating")
	appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted) // Pending → Running
	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind: journal.KindCompensationStarted, // Running → Compensating
	})
	if err != nil {
		t.Fatalf(fmtAppendCompErr, err)
	}

	// Now in Compensating — a forward step must be rejected.
	_, err = j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("Append(KindStepStarted) while Compensating should be rejected, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append(KindStepStarted) while Compensating: want KindInvalid error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// NEW: LeaderHandoff_ReadsCompensatingPhase (F2 regression)
// ---------------------------------------------------------------------------

// conformLeaderHandoffReadsCompensatingPhase is the F2 regression: leader A
// drives the saga to Compensating, then its lease expires. Leader B reclaims and
// must observe StatusCompensating (not Running). B then drives to terminal.
func conformLeaderHandoffReadsCompensatingPhase(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-handoff-compensating", clk.Now())
	mustEnqueue(t, j, inst)

	// Leader A claims and drives to Compensating.
	claimedA, leaseA, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedA) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimedA))
	}
	appendStep(t, j, inst.ID, leaseA, journal.KindStepStarted) // Pending → Running
	_, err = j.Append(context.Background(), inst.ID, leaseA, journal.Event{
		Kind: journal.KindCompensationStarted, // Running → Compensating
	})
	if err != nil {
		t.Fatalf(fmtAppendCompErr, err)
	}

	// A's lease expires.
	clk.Advance(shortLease + time.Second)

	// Leader B reclaims. Must see StatusCompensating.
	claimedB, leaseB, err := j.ClaimPending(context.Background(), 10, shortLease)
	if err != nil || len(claimedB) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimedB))
	}
	ciB := findClaimed(t, claimedB, inst.ID)
	if ciB.Instance.Status != saga.StatusCompensating {
		t.Errorf("leader B sees status=%s; want StatusCompensating", ciB.Instance.Status)
	}

	// B drives to terminal (Compensating → Compensated is legal).
	ok, err := j.MarkTerminal(context.Background(), inst.ID, leaseB, saga.StatusCompensated)
	if err != nil || !ok {
		t.Fatalf("leader B MarkTerminal(Compensated): ok=%v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// NEW: ClaimPending_NonPositiveLeaseDuration_KindInvalid
// ---------------------------------------------------------------------------

// conformClaimPendingNonPositiveLeaseDuration verifies that ClaimPending with
// leaseDuration 0 or negative returns a KindInvalid error.
func conformClaimPendingNonPositiveLeaseDuration(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	for _, d := range []time.Duration{0, -time.Second} {
		_, _, err := j.ClaimPending(context.Background(), 1, d)
		if err == nil {
			t.Fatalf("ClaimPending(leaseDuration=%s) should return error, got nil", d)
		}
		if !isKindInvalid(err) {
			t.Errorf("ClaimPending(leaseDuration=%s): want KindInvalid error, got %v", d, err)
		}
	}
}

// ---------------------------------------------------------------------------
// NEW: Heartbeat_NonPositiveLeaseDuration_KindInvalid
// ---------------------------------------------------------------------------

// conformHeartbeatNonPositiveLeaseDuration verifies that Heartbeat with
// leaseDuration 0 or negative returns (false, KindInvalid error).
func conformHeartbeatNonPositiveLeaseDuration(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := claimOne(t, j, clk, "inst-hb-nonpositive-lease")

	for _, d := range []time.Duration{0, -time.Second} {
		ok, err := j.Heartbeat(context.Background(), ci.Instance.ID, ci.LeaseID, d)
		if err == nil {
			t.Fatalf("Heartbeat(leaseDuration=%s) should return error, got nil", d)
		}
		if !isKindInvalid(err) {
			t.Errorf("Heartbeat(leaseDuration=%s): want KindInvalid error, got %v", d, err)
		}
		if ok {
			t.Errorf("Heartbeat(leaseDuration=%s): want ok=false, got ok=true", d)
		}
	}
}

// ---------------------------------------------------------------------------
// PR-04 carry-over: lease exact-equality boundary + error precedence + payload cap
// ---------------------------------------------------------------------------

// conformAppendExactlyAtLeaseExpiryStillValid asserts the journal's lease
// boundary contract: at exact equality (clk.Now() == leaseExpiresAt) the
// lease is STILL VALID (`!Before(now)` semantic). Append at this instant
// must succeed; the boundary is symmetric across mem and PG.
func conformAppendExactlyAtLeaseExpiryStillValid(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const lease = shortLease // package-level const (== 10s)
	startedAt := clk.Now()
	inst := NewInstanceFixture(t, "inst-expiry-append-equal", startedAt)
	mustEnqueue(t, j, inst)

	claimed, _, err := j.ClaimPending(context.Background(), 1, lease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	// Advance EXACTLY to the lease expiry instant.
	clk.Advance(lease)

	_, err = j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
	})
	if err != nil {
		t.Errorf("Append at exact lease expiry (now == leaseExpiresAt): want success, got %v", err)
	}
}

// conformClaimPendingExactlyAtLeaseExpiryNotEligible asserts the converse
// boundary: at exact equality the lease is still valid, so another claim
// MUST NOT reclaim it.
func conformClaimPendingExactlyAtLeaseExpiryNotEligible(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const lease = shortLease // package-level const (== 10s)
	inst := NewInstanceFixture(t, "inst-expiry-claim-equal", clk.Now())
	mustEnqueue(t, j, inst)

	claimedA, leaseA, err := j.ClaimPending(context.Background(), 1, lease)
	if err != nil || len(claimedA) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimedA))
	}
	_ = leaseA

	clk.Advance(lease) // exactly at expiry

	claimedB, _, err := j.ClaimPending(context.Background(), 1, lease)
	if err != nil {
		t.Fatalf("ClaimPending at exact expiry: %v", err)
	}
	if len(claimedB) != 0 {
		t.Errorf("ClaimPending at exact lease expiry: want no reclaim (lease still valid), got %d", len(claimedB))
	}
}

// conformHeartbeatExactlyAtLeaseExpiryStillExtends asserts Heartbeat at
// exact equality extends the lease (still valid → ok=true).
func conformHeartbeatExactlyAtLeaseExpiryStillExtends(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	const lease = shortLease // package-level const (== 10s)
	inst := NewInstanceFixture(t, "inst-expiry-hb-equal", clk.Now())
	mustEnqueue(t, j, inst)

	claimed, _, err := j.ClaimPending(context.Background(), 1, lease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	clk.Advance(lease)

	ok, err := j.Heartbeat(context.Background(), ci.Instance.ID, ci.LeaseID, lease)
	if err != nil {
		t.Errorf("Heartbeat at exact expiry: unexpected error %v", err)
	}
	if !ok {
		t.Errorf("Heartbeat at exact lease expiry: want ok=true (lease still valid), got ok=false")
	}
}

// conformAppendUnknownWithBadPayloadPrefersNotFound asserts that Append
// applied to a never-enqueued instance returns ErrSagaNotFound (KindNotFound)
// even when the event payload is also invalid — instance-existence and lease
// fence take precedence over event-shape validation. This contract makes
// operator routing predictable across mem and PG.
func conformAppendUnknownWithBadPayloadPrefersNotFound(t *testing.T, factory Factory) {
	t.Helper()
	j, _, cleanup := factory(t)
	defer cleanup()

	_, err := j.Append(context.Background(), neverEnqueuedID, anyLease, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
		Payload:  []byte(`not-valid-json`), // also invalid; precedence test
	})
	if err == nil {
		t.Fatal("Append on unknown instance with bad payload: want error, got nil")
	}
	if !isKindNotFound(err) {
		t.Errorf("want KindNotFound (precedence over payload validation), got %v", err)
	}
	requireCode(t, err, errcode.ErrSagaNotFound)
}

// ---------------------------------------------------------------------------
// F6 (#1210): KindStepCompensationFailed phase legality
// ---------------------------------------------------------------------------

// conformStepCompensationFailedLegalDuringCompensating asserts that
// KindStepCompensationFailed is accepted while the saga is in the Compensating
// phase (status unchanged — same as KindStepCompensated).
func conformStepCompensationFailedLegalDuringCompensating(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := driveToCompensating(t, j, clk, "inst-step-comp-failed-legal")

	_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepCompensationFailed,
		StepName: stepOne,
	})
	if err != nil {
		t.Fatalf("Append(KindStepCompensationFailed) while Compensating: want success, got %v", err)
	}

	// Status must still be Compensating (non-terminal, unchanged).
	claimed, _, err := j.ClaimPending(context.Background(), 100, shortLease)
	// After a successful Append the instance still has an active lease (ci.LeaseID),
	// so it is not re-claimable; but we can verify via Load that no terminal event was written.
	if err != nil {
		t.Fatalf("ClaimPending after step-compensation-failed append: %v", err)
	}
	_ = claimed // may be empty — instance still holds its lease
	if loadHasTerminal(t, j, ci.Instance.ID) {
		t.Error("KindStepCompensationFailed must not write a terminal event; status should remain Compensating")
	}
}

// conformStepCompensationFailedOutsideCompensating asserts that
// KindStepCompensationFailed is rejected when the saga is NOT in the
// Compensating phase (Pending or Running).
func conformStepCompensationFailedOutsideCompensating(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	cases := []struct {
		phase      string
		instanceID string
		advance    func(t *testing.T, ci journal.ClaimedInstance) // Pending → target phase; nil = stay Pending
	}{
		{phase: "Pending", instanceID: "inst-step-comp-failed-outside-pending", advance: nil},
		{
			phase:      "Running",
			instanceID: "inst-step-comp-failed-outside-running",
			advance: func(t *testing.T, ci journal.ClaimedInstance) {
				appendStep(t, j, ci.Instance.ID, ci.LeaseID, journal.KindStepStarted)
			},
		},
	}
	for _, tc := range cases {
		ci := claimOne(t, j, clk, tc.instanceID)
		if tc.advance != nil {
			tc.advance(t, ci)
		}
		_, err := j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
			Kind:     journal.KindStepCompensationFailed,
			StepName: stepOne,
		})
		if err == nil {
			t.Fatalf("Append(KindStepCompensationFailed) on a %s instance should be rejected, got nil", tc.phase)
		}
		if !isKindInvalid(err) {
			t.Errorf("Append(KindStepCompensationFailed) on %s: want KindInvalid error, got %v", tc.phase, err)
		}
	}
}

// conformSagaCompensationFailedTerminal verifies the full
// Compensating → StatusCompensationFailed terminal path:
//  1. MarkTerminal writes KindSagaCompensationFailed to the log.
//  2. The lease is released on success (instance not re-claimable by old lease).
//  3. A subsequent Append with the old lease is rejected (terminal, no lease).
func conformSagaCompensationFailedTerminal(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	ci := driveToCompensating(t, j, clk, "inst-saga-comp-failed-terminal")

	ok, err := j.MarkTerminal(context.Background(), ci.Instance.ID, ci.LeaseID, saga.StatusCompensationFailed)
	if err != nil {
		t.Fatalf("MarkTerminal(Compensating→CompensationFailed): %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal(Compensating→CompensationFailed): expected ok=true")
	}

	// Terminal event must be KindSagaCompensationFailed.
	kind := loadTerminalKind(t, j, ci.Instance.ID)
	if kind != journal.KindSagaCompensationFailed {
		t.Errorf("terminal event: want KindSagaCompensationFailed, got %s", kind)
	}

	// Lease released: subsequent Append with the old lease must be rejected.
	_, err = j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepCompensated,
		StepName: stepOne,
	})
	if err == nil {
		t.Fatal("Append after MarkTerminal(CompensationFailed): want conflict error, got nil")
	}
	if !isKindConflict(err) {
		t.Errorf("Append after terminal: want KindConflict error (stale/no lease), got %v", err)
	}
}

// conformAppendPayloadOverMaxBytes asserts the MaxPayloadBytes contract is
// enforced at the journal boundary (rejected before any DB / lock write).
func conformAppendPayloadOverMaxBytes(t *testing.T, factory Factory) {
	t.Helper()
	j, clk, cleanup := factory(t)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-payload-overcap", clk.Now())
	mustEnqueue(t, j, inst)
	claimed, _, err := j.ClaimPending(context.Background(), 1, shortLease)
	if err != nil || len(claimed) == 0 {
		t.Fatalf(fmtClaimErr, err, len(claimed))
	}
	ci := findClaimed(t, claimed, inst.ID)

	// Build a payload exactly MaxPayloadBytes+1 long. Use a JSON object
	// shape that would otherwise parse, so the size check is what fires.
	const headerLen = 6 // `{"x":"`
	const footerLen = 2 // `"}`
	padLen := journal.MaxPayloadBytes - headerLen - footerLen + 1
	pad := make([]byte, padLen)
	for i := range pad {
		pad[i] = 'a'
	}
	over := append(append([]byte(`{"x":"`), pad...), []byte(`"}`)...)
	if len(over) != journal.MaxPayloadBytes+1 {
		t.Fatalf("test fixture length mismatch: got %d want %d", len(over), journal.MaxPayloadBytes+1)
	}

	_, err = j.Append(context.Background(), ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
		Payload:  over,
	})
	if err == nil {
		t.Fatal("Append with oversize payload: want error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("Append with oversize payload: want KindInvalid, got %v", err)
	}
}
