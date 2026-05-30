package saga

// integration_test.go covers Coordinator integration scenarios:
//   - TestIntegration_HappyPath1Step (§220 scenario 1)
//   - TestIntegration_StepRunError (§220 scenario 2)
//   - TestIntegration_TotalSagaTimeout (§220 scenario 4)
//   - TestIntegration_ClaimContention (§220 scenario 6)
//   - TestIntegration_ResumeAfterRestart (§220 scenario 7)
//   - TestIntegration_PanicRecovery (§220 scenario 8)
//   - TestIntegration_Compensation_LeaseLost_ResumesOnReclaim (#1210 C3 — compensation-phase lease-lost recovery)
//
// §220 scenarios 3 (retry budget) and 5 (multi-step compensation walk)
// are covered by white-box unit tests in coordinator_test.go and
// executor_test.go; the integration tier focuses on cross-coordinator
// goroutine paths.
//
// Fake helpers live in testfakes_test.go (same package, test-only).

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// File-local duration consts for values not present in testtime.
const (
	// testLeaseAdvance is used in TestIntegration_ResumeAfterRestart to advance
	// the clock past the 60s lease expiry so coordinator 2 can re-claim.
	testLeaseAdvance = 65 * time.Second
)

// ---------------------------------------------------------------------------
// TestMain — goleak guard for entire package
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---------------------------------------------------------------------------
// harness helpers
// ---------------------------------------------------------------------------

// testHarness bundles all the fake dependencies needed by a Coordinator under
// test. Build via newTestHarness.
type testHarness struct {
	j       *journal.MemJournal
	clk     *clockmock.FakeClock
	emitter *safeFakeEmitter
	tx      *safeFakeTxRunner
	reg     *ksaga.InMemoryRegistry
	disp    *recordingDispatcher
	coord   *Coordinator
}

// newTestHarness constructs a harness with a fresh MemJournal, FakeClock,
// safeFakeEmitter, safeFakeTxRunner and recordingDispatcher. If defs is
// non-empty they are pre-registered. The Coordinator is pre-built with a
// test-friendly config (fast poll so clock-driven ticks happen with small advances).
func newTestHarness(t *testing.T, defs ...*ksaga.Definition) *testHarness {
	t.Helper()

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	reg, err := ksaga.NewInMemoryRegistry(defs...)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}

	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()
	disp := &recordingDispatcher{}

	cfg := Config{
		PollInterval:      testtime.D10ms, // fast tick for clock-driven tests
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	c, err := NewCoordinator(j, tx, em, reg, clk,
		WithConfig(cfg),
		WithDispatcher(disp),
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	return &testHarness{
		j:       j,
		clk:     clk,
		emitter: em,
		tx:      tx,
		reg:     reg,
		disp:    disp,
		coord:   c,
	}
}

// newInstance creates a saga.Instance with a UUID id and the given definitionID.
func newInstance(t *testing.T, defID idutil.SafeID, now time.Time) ksaga.Instance {
	t.Helper()
	id, err := idutil.NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	return ksaga.NewInstance(idutil.SafeID(id), defID, now)
}

// testPollInterval is the clock advance used by tickOnceAndWait to trigger one
// tick of the coordinator (must match the harness Config.PollInterval = 10ms).
const testPollInterval = 10 * time.Millisecond

// tickOnceAndWait advances the FakeClock by testPollInterval so the tickLoop
// fires, then waits until cond returns true (polling real-time). It fails the
// test if cond is not satisfied within 2s.
func tickOnceAndWait(t *testing.T, clk *clockmock.FakeClock, cond func() bool) {
	t.Helper()
	clk.Advance(testPollInterval)
	testwait.External(t, "tick-effect-observed", cond, testtime.D2s, testtime.D2ms)
}

// startCoord starts the coordinator in a background goroutine and registers
// cleanup via t.Cleanup. Returns a cancel function that triggers a clean Stop.
//
// It waits not only for the Ready() channel but also for the coordinator's
// tickLoop to register its ticker with the FakeClock (PendingTickers >= 1),
// ensuring clock Advance calls are not missed.
func startCoord(t *testing.T, c *Coordinator) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()

	// Wait until the coordinator's ready channel is closed.
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator did not become ready within 2s")
	}

	// Wait for tickLoop to register its ticker with the FakeClock. readyCh is
	// closed before the goroutine is launched, so there is a brief window where
	// no tickers are registered yet. Without this wait, a subsequent clk.Advance
	// would not fire any ticker.
	clk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)

	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
		defer stopCancel()
		if err := c.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("cleanup Stop: %v", err)
		}
		select {
		case <-done:
		case <-time.After(testtime.D3s):
			t.Error("coordinator goroutine did not exit within 3s after Stop")
		}
	})

	return cancel
}

// ---------------------------------------------------------------------------
// Scenario 1 — Happy path 1-step saga
// ---------------------------------------------------------------------------

// TestIntegration_HappyPath1Step verifies that a 1-step saga with a successful
// Run function produces:
//   - journal events: [KindStepCompleted v1, KindSagaSucceeded v2]
//   - emitter captures 1 entry (step-completed outbox event)
//   - dispatcher.Kick count == 1 (fired from AfterCommit hook)
//   - instance Status == StatusSucceeded after tick
func TestIntegration_HappyPath1Step(t *testing.T) {
	const defID idutil.SafeID = "happypath1"
	stepPayload := []byte(`{"result":"ok"}`)

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return stepPayload, nil
				},
			},
		},
	}

	h := newTestHarness(t, def)
	cancel := startCoord(t, h.coord)
	defer cancel()

	inst := newInstance(t, defID, h.clk.Now())
	if err := h.j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Advance clock by PollInterval; wait for journal to contain 2 events
	// (StepCompleted + SagaSucceeded) written by commitStep + MarkTerminal.
	tickOnceAndWait(t, h.clk, func() bool {
		evs, err := h.j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 2
	})

	evs, err := h.j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("journal event count = %d, want 2", len(evs))
	}
	if evs[0].Kind != journal.KindStepCompleted {
		t.Errorf("evs[0].Kind = %s, want step_completed", evs[0].Kind)
	}
	if evs[0].Version != 1 {
		t.Errorf("evs[0].Version = %d, want 1", evs[0].Version)
	}
	if evs[1].Kind != journal.KindSagaSucceeded {
		t.Errorf("evs[1].Kind = %s, want saga_succeeded", evs[1].Kind)
	}
	if evs[1].Version != 2 {
		t.Errorf("evs[1].Version = %d, want 2", evs[1].Version)
	}

	// Emitter: 1 step-completed entry captured.
	entries := h.emitter.Snapshot()
	if len(entries) != 1 {
		t.Errorf("emitter entries = %d, want 1", len(entries))
	}

	// Dispatcher Kick count == 1 (AfterCommit hook fired once for the commit).
	if got := h.disp.KickCount(); got != 1 {
		t.Errorf("KickCount = %d, want 1", got)
	}

	// Status == Succeeded: ClaimPending returns empty (terminal instance excluded).
	claimed, _, err := h.j.ClaimPending(context.Background(), 16, testtime.D30s)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimPending returned %d items, want 0 (terminal instance excluded)", len(claimed))
	}
}

// ---------------------------------------------------------------------------
// Scenario 2 — Step.Run returns error
// ---------------------------------------------------------------------------

// TestIntegration_StepRunError verifies that a step failure produces:
//   - journal events: [KindStepFailed v1, KindSagaFailed v2]
//   - emitter captures 0 entries (no step-completed event on failure)
//   - dispatcher.Kick count == 1 (commit happened)
//   - instance is terminal (StatusFailed)
func TestIntegration_StepRunError(t *testing.T) {
	const defID idutil.SafeID = "stepfail1"
	stepErr := errors.New("step intentionally failed")

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, stepErr
				},
			},
		},
	}

	h := newTestHarness(t, def)
	cancel := startCoord(t, h.coord)
	defer cancel()

	inst := newInstance(t, defID, h.clk.Now())
	if err := h.j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait for 2 journal events: StepFailed + SagaFailed.
	tickOnceAndWait(t, h.clk, func() bool {
		evs, err := h.j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 2
	})

	evs, err := h.j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("journal event count = %d, want 2", len(evs))
	}
	if evs[0].Kind != journal.KindStepFailed {
		t.Errorf("evs[0].Kind = %s, want step_failed", evs[0].Kind)
	}
	if evs[1].Kind != journal.KindSagaFailed {
		t.Errorf("evs[1].Kind = %s, want saga_failed", evs[1].Kind)
	}

	// No outbox entry on failure.
	if got := len(h.emitter.Snapshot()); got != 0 {
		t.Errorf("emitter entries = %d, want 0", got)
	}

	// Dispatcher still kicked once (commit path ran).
	if got := h.disp.KickCount(); got != 1 {
		t.Errorf("KickCount = %d, want 1", got)
	}

	// Instance is terminal; ClaimPending returns nothing.
	claimed, _, err := h.j.ClaimPending(context.Background(), 16, testtime.D30s)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimPending returned %d items, want 0", len(claimed))
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 — Total saga timeout
// ---------------------------------------------------------------------------

// TestIntegration_TotalSagaTimeout verifies that when Definition.Timeout is
// exceeded (clock advanced past it before any tick processes the instance), the
// Coordinator marks the instance Expired without calling Step.Run.
func TestIntegration_TotalSagaTimeout(t *testing.T) {
	const defID idutil.SafeID = "satimeout1"
	stepRunCalled := false

	def := &ksaga.Definition{
		ID:      defID,
		Timeout: testtime.D10ms,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					stepRunCalled = true
					return nil, nil
				},
			},
		},
	}

	h := newTestHarness(t, def)
	cancel := startCoord(t, h.coord)
	defer cancel()

	inst := newInstance(t, defID, h.clk.Now())
	if err := h.j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Advance clock by 1 hour — well past the 10ms timeout. The FakeClock
	// fires the ticker (multiple intervals coalesce to 1 tick on the channel).
	// The tickLoop goroutine will claim the instance, see elapsed > Timeout,
	// and immediately call markTerminal(Expired).
	h.clk.Advance(testtime.D1h)

	// Poll for the Expired terminal event (real-time loop; goroutine needs CPU).
	var evs []journal.Event
	testwait.External(t, "saga-expired-event-written",
		func() bool {
			var err error
			evs, err = h.j.Load(context.Background(), inst.ID)
			return err == nil && len(evs) > 0 && evs[len(evs)-1].Kind == journal.KindSagaExpired
		},
		testtime.D2s, testtime.D2ms)

	last := evs[len(evs)-1]
	if last.Kind != journal.KindSagaExpired {
		t.Errorf("last event = %s, want saga_expired", last.Kind)
	}

	// Step.Run must NOT have been called.
	if stepRunCalled {
		t.Error("Step.Run was called but should not have been (timeout before execution)")
	}
}

// ---------------------------------------------------------------------------
// Scenario 6 — Claim contention
// ---------------------------------------------------------------------------

// TestIntegration_ClaimContention verifies that two Coordinators sharing one
// MemJournal drive a single enqueued instance exactly once across both:
// total Kick count across both dispatchers == 1 (only one coordinator wins
// the claim).
func TestIntegration_ClaimContention(t *testing.T) {
	const defID idutil.SafeID = "claimcontention"

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, nil
				},
			},
		},
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    1, // each coordinator claims at most 1 instance
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	disp1 := &recordingDispatcher{}
	em1 := newSafeFakeEmitter()
	tx1 := newSafeFakeTxRunner()
	c1, err := NewCoordinator(j, tx1, em1, reg, clk, WithConfig(cfg), WithDispatcher(disp1))
	if err != nil {
		t.Fatalf("NewCoordinator c1: %v", err)
	}

	disp2 := &recordingDispatcher{}
	em2 := newSafeFakeEmitter()
	tx2 := newSafeFakeTxRunner()
	c2, err := NewCoordinator(j, tx2, em2, reg, clk, WithConfig(cfg), WithDispatcher(disp2))
	if err != nil {
		t.Fatalf("NewCoordinator c2: %v", err)
	}

	inst := newInstance(t, defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Start both coordinators.
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- c1.Start(ctx1) }()
	go func() { done2 <- c2.Start(ctx2) }()

	select {
	case <-c1.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("c1 not ready")
	}
	select {
	case <-c2.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("c2 not ready")
	}

	// Wait for both coordinators' tickLoop tickers to register (1 ticker per coordinator).
	testwait.External(t, "both-coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Advance clock to trigger one tick; wait for instance to become terminal.
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		if err != nil || len(evs) == 0 {
			return false
		}
		return evs[len(evs)-1].Kind.IsTerminal()
	})

	// Stop both coordinators.
	stopTwoCoordinators(t, c1, c2, cancel1, cancel2, done1, done2)

	// Total Kick count across both dispatchers == 1 (only one commit happened).
	totalKicks := disp1.KickCount() + disp2.KickCount()
	if totalKicks != 1 {
		t.Errorf("total KickCount = %d, want 1", totalKicks)
	}

	// Journal has exactly 2 events: StepCompleted + SagaSucceeded.
	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("journal event count = %d, want 2", len(evs))
	}
}

// ---------------------------------------------------------------------------
// Scenario 7 — Resume after restart
// ---------------------------------------------------------------------------

// TestIntegration_ResumeAfterRestart verifies that a 2-step saga can be paused
// mid-execution (after step 1 completes) and resumed by a new Coordinator
// instance sharing the same MemJournal.
//
// Expected journal: [StepCompleted v1, StepCompleted v2, SagaSucceeded v3].
func TestIntegration_ResumeAfterRestart(t *testing.T) {
	const defID idutil.SafeID = "resume2step"
	step2Ran := false
	var step2ReceivedState []byte

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return []byte(`{"step":1}`), nil
				},
			},
			{
				Name: "step2",
				Run: func(_ context.Context, _ *ksaga.Instance, prevState []byte) ([]byte, error) {
					step2Ran = true
					step2ReceivedState = prevState
					return []byte(`{"step":2}`), nil
				},
			},
		},
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)
	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	inst := newInstance(t, defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Phase 1 — coordinator c1 drives step 1 only.
	c1, c1Handles := startCoordinatorForResume(t, j, reg, clk, cfg, "c1")
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 1 && evs[0].Kind == journal.KindStepCompleted
	})
	stopCoordinatorAndWait(t, c1, c1Handles, "c1")

	// Phase 2 — coordinator c2 resumes from journal and drives step 2.
	c2, c2Handles := startCoordinatorForResume(t, j, reg, clk, cfg, "c2")
	defer stopCoordinatorAndWait(t, c2, c2Handles, "c2")

	// Past the 60s lease expiry so c2 can re-claim.
	clk.Advance(testLeaseAdvance)
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 3
	})

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertResumeJournal(t, evs, step2Ran, step2ReceivedState)
}

// resumeCoordinatorHandles bundles the goroutine + cancel + done channel for
// one coordinator in the resume-after-restart scenario. Used by
// startCoordinatorForResume / stopCoordinatorAndWait.
type resumeCoordinatorHandles struct {
	cancel context.CancelFunc
	done   chan error
}

// startCoordinatorForResume builds a coordinator, starts it in a goroutine,
// waits for Ready, then waits for the tickLoop ticker to register on the clock.
// The returned handles are passed to stopCoordinatorAndWait to clean up.
func startCoordinatorForResume(
	t *testing.T,
	j journal.Journal,
	reg ksaga.Resolver,
	clk *clockmock.FakeClock,
	cfg Config,
	name string,
) (*Coordinator, resumeCoordinatorHandles) {
	t.Helper()
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(cfg), WithDispatcher(&recordingDispatcher{}))
	if err != nil {
		t.Fatalf("NewCoordinator %s: %v", name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		cancel()
		t.Fatalf("%s not ready", name)
	}
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)
	return c, resumeCoordinatorHandles{cancel: cancel, done: done}
}

// stopCoordinatorAndWait cancels the coordinator's ctx, calls Stop with a
// generous budget, and waits for the Start goroutine to exit. Multiple
// "did the goroutine exit" branches are isolated here so the calling test
// stays under the cognitive-complexity ceiling.
func stopCoordinatorAndWait(t *testing.T, c *Coordinator, h resumeCoordinatorHandles, name string) {
	t.Helper()
	h.cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("%s Stop: %v", name, err)
	}
	select {
	case <-h.done:
	case <-time.After(testtime.D3s):
		t.Errorf("%s goroutine did not exit", name)
	}
}

// assertResumeJournal validates the post-resume journal contents (3 events:
// StepCompleted v1, StepCompleted v2, SagaSucceeded v3) + step2 invocation +
// prevState passthrough. Kept here so the main test stays declarative.
func assertResumeJournal(t *testing.T, evs []journal.Event, step2Ran bool, step2ReceivedState []byte) {
	t.Helper()
	if len(evs) != 3 {
		t.Fatalf("journal event count = %d, want 3; events: %v", len(evs), evs)
	}
	expected := []struct {
		kind    journal.EventKind
		version int64
		label   string
	}{
		{journal.KindStepCompleted, 1, "step_completed v1"},
		{journal.KindStepCompleted, 2, "step_completed v2"},
		{journal.KindSagaSucceeded, 3, "saga_succeeded v3"},
	}
	for i, want := range expected {
		if evs[i].Kind != want.kind || evs[i].Version != want.version {
			t.Errorf("evs[%d] = {%s, v%d}, want {%s}", i, evs[i].Kind, evs[i].Version, want.label)
		}
	}
	if !step2Ran {
		t.Error("step2 was not executed by coordinator 2")
	}
	// Verify that foldEvents correctly passed step 1's output payload to step 2
	// (L3 replay reconstruction discipline: prevState passthrough).
	if expected := []byte(`{"step":1}`); !bytes.Equal(step2ReceivedState, expected) {
		t.Errorf("step2 prevState = %q, want %q", step2ReceivedState, expected)
	}
}

// ---------------------------------------------------------------------------
// Scenario 8 — Panic recovery in Step.Run
// ---------------------------------------------------------------------------

// TestIntegration_PanicRecovery verifies that a panic inside Step.Run is
// recovered by safeRun, converted to an error, and drives the normal failure
// path (commitStepFailed → journal.Append(StepFailed) + journal.MarkTerminal(Failed)).
// No goroutines leak after the test.
//
// safeRun returns (nil, err) on panic; driveOne then opens the tx via
// txRunner.RunInTx and calls commitStepFailed which writes StepFailed +
// SagaFailed to the journal — the instance becomes terminal. Outbox emitter
// receives no entries (only successful steps emit).
func TestIntegration_PanicRecovery(t *testing.T) {
	const defID idutil.SafeID = "panicrecovery"
	stepCallCount := 0

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "panicstep",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					stepCallCount++
					panic("test panic from step")
				},
			},
		},
	}

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()
	disp := &recordingDispatcher{}

	c, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(cfg), WithDispatcher(disp))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := newInstance(t, defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator not ready")
	}

	// Wait for tickLoop ticker to be registered with the FakeClock.
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)

	// Advance clock to trigger the tick. safeRun recovers the panic, returns
	// (nil, err). driveOne opens the tx via RunInTx → commitStepFailed →
	// journal.Append(StepFailed) + journal.MarkTerminal(Failed).
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 2
	})

	assertPanicRecoveryJournal(t, j, inst.ID)

	// No outbox entries on failure path.
	if got := em.Count(); got != 0 {
		t.Errorf("emitter entries = %d, want 0", got)
	}

	// Instance is terminal; ClaimPending returns nothing.
	claimed, _, err := j.ClaimPending(context.Background(), 16, testtime.D30s)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimPending returned %d items, want 0 (terminal after panic recovery)", len(claimed))
	}

	// Dispatcher kicked once (AfterCommit from commitStepFailed).
	if got := disp.KickCount(); got != 1 {
		t.Errorf("KickCount = %d, want 1", got)
	}

	stopCoordinator(t, c, cancel, done)

	// Step was called exactly once (the first tick triggered it).
	if stepCallCount != 1 {
		t.Errorf("step call count = %d, want 1", stepCallCount)
	}
}

// assertPanicRecoveryJournal verifies the journal contains exactly StepFailed + SagaFailed.
func assertPanicRecoveryJournal(t *testing.T, j *journal.MemJournal, instID idutil.SafeID) {
	t.Helper()
	evs, err := j.Load(context.Background(), instID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("journal event count = %d, want 2 (StepFailed + SagaFailed)", len(evs))
	}
	if evs[0].Kind != journal.KindStepFailed {
		t.Errorf("evs[0].Kind = %s, want step_failed", evs[0].Kind)
	}
	if evs[1].Kind != journal.KindSagaFailed {
		t.Errorf("evs[1].Kind = %s, want saga_failed", evs[1].Kind)
	}
}

// stopCoordinator cancels ctx, stops the coordinator, and waits for the goroutine to exit.
func stopCoordinator(t *testing.T, c *Coordinator, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(testtime.D3s):
		t.Error("coordinator goroutine did not exit")
	}
}

// stopTwoCoordinators stops two coordinators concurrently and waits for both goroutines.
func stopTwoCoordinators(t *testing.T, c1, c2 *Coordinator,
	cancel1, cancel2 context.CancelFunc, done1, done2 <-chan error,
) {
	t.Helper()
	cancel1()
	cancel2()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c1.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("c1 Stop: %v", err)
	}
	if err := c2.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("c2 Stop: %v", err)
	}
	select {
	case <-done1:
	case <-time.After(testtime.D3s):
		t.Error("c1 goroutine did not exit")
	}
	select {
	case <-done2:
	case <-time.After(testtime.D3s):
		t.Error("c2 goroutine did not exit")
	}
}

// ---------------------------------------------------------------------------
// Compensation lease-lost recovery path (#1181 F1 + F2)
// ---------------------------------------------------------------------------

// TestIntegration_Compensation_LeaseLost_ResumesOnReclaim verifies that when
// a coordinator loses its lease mid-compensation (heartbeat returns ok=false),
// it abandons the walk without writing a terminal event, and a second
// coordinator that re-claims the same instance (now projected as
// StatusCompensating) resumes compensation from the remaining steps and drives
// the saga to a terminal state.
//
// The integration level here is "two coordinators, shared mem journal, white-box
// driveOne calls" — the goroutine-level lease hand-off is exercised by
// TestSagaLeaderElect_TwoCoordinators_PG_ExactlyOnce in tests/integration/sagaleader/.
//
// Implementation notes:
//   - A 2-step saga is used: step1 succeeds (has Compensate), step2 fails
//     (triggers compensation). The reverse walk only visits committed steps,
//     so only step1.Compensate is called during the compensation walk.
//   - staleAfterFirstHBJournal (declared in coordinator_test.go) injects
//     ok=false after SetStale() is called, simulating lease expiry detected by
//     the heartbeat goroutine.
//   - coordinator-1 runs driveOne in a background goroutine. step1.Compensate
//     signals the test goroutine, which arms stale + advances the FakeClock to
//     trigger the heartbeat ticker. The goroutine observes ok=false →
//     cancelCause(errLeaseLost) → step1.Compensate's ctx is canceled →
//     reverseWalkCompensate exits early → runCompensation returns nil (no terminal
//     written).
//   - The lease is expired via clock advance so coordinator-2 can re-claim.
//     coordinator-2's driveOne sees ci.Instance.Status == StatusCompensating →
//     recovery path → drives step1.Compensate again → StatusCompensated.
//
// makeStep1CompensateFn returns a Compensate function for step1 that:
// - On first call (coordinator-1): signals compensationStarted, then blocks until ctx is done.
// - On subsequent calls (coordinator-2 recovery): returns immediately.
// compensationStarted is a buffered channel (cap=1); non-blocking sends fall through.
func makeStep1CompensateFn(compensationStarted chan<- struct{}, calls *atomic.Int64) func(context.Context, *ksaga.Instance, []byte) error {
	return func(ctx context.Context, _ *ksaga.Instance, _ []byte) error {
		n := calls.Add(1)
		if n == 1 {
			select {
			case compensationStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
}

// claimAndDriveOne claims exactly one pending instance from j and drives it synchronously.
func claimAndDriveOne(t *testing.T, c *Coordinator, j *journal.MemJournal, label string) {
	t.Helper()
	claimed, _, err := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending %s: %v / %d", label, err, len(claimed))
	}
	if err := c.driveOne(context.Background(), claimed[0]); err != nil {
		t.Fatalf("driveOne %s: %v", label, err)
	}
}

// claimOneAsync claims exactly one pending instance and starts driveOne in a goroutine.
// Returns the error channel.
func claimOneAsync(t *testing.T, c *Coordinator, j *journal.MemJournal, label string) <-chan error {
	t.Helper()
	claimed, _, err := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("ClaimPending %s: %v / %d", label, err, len(claimed))
	}
	ch := make(chan error, 1)
	go func() { ch <- c.driveOne(context.Background(), claimed[0]) }()
	return ch
}

func TestIntegration_Compensation_LeaseLost_ResumesOnReclaim(t *testing.T) {
	const defID idutil.SafeID = "compleaselostrecovery"

	clk := clockmock.New(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	memJ, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	// staleJ wraps memJ: after SetStale() the next Heartbeat call returns ok=false,
	// causing the RunWithHeartbeat goroutine to cancelCause(errLeaseLost).
	staleJ := &staleAfterFirstHBJournal{MemJournal: memJ}

	compensationStarted := make(chan struct{}, 1)
	var step1CompensateCalls atomic.Int64

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name:       "step1",
				Run:        func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return []byte(`{"s":1}`), nil },
				Compensate: makeStep1CompensateFn(compensationStarted, &step1CompensateCalls),
			},
			{
				Name: "step2",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, errors.New("step2 deliberately fails to trigger compensation")
				},
				// No Compensate — step2 was not committed so it is not in the
				// reverse walk regardless. (Nil Compensate is a no-op.)
			},
		},
	}

	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}

	// Short heartbeat interval so the goroutine fires after one clock tick.
	cfg1 := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D5ms,
	}
	c1, err := NewCoordinator(staleJ, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(cfg1))
	if err != nil {
		t.Fatalf("NewCoordinator c1: %v", err)
	}

	inst := newInstance(t, defID, clk.Now())
	if err := memJ.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Phase 1 — c1 drives step1 (succeeds) synchronously.
	clk.Advance(testtime.D60s + testtime.D1ms) // start fresh lease window
	claimAndDriveOne(t, c1, memJ, "phase1 (step1)")

	// Phase 2 — c1 drives step2 (fails) → enters compensation → loses lease.
	// driveOne runs in a background goroutine so the test can advance the clock
	// mid-flight after step1.Compensate signals.
	clk.Advance(testtime.D60s + testtime.D1ms)
	driveErrCh := claimOneAsync(t, c1, memJ, "phase2")

	// Wait until compensation walk has started (step1.Compensate signaled).
	select {
	case <-compensationStarted:
	case <-time.After(testtime.D2s):
		t.Fatal("compensation walk did not start within 2s")
	}

	// Arm the stale flag. Wait for the heartbeat ticker to be registered with
	// the FakeClock (the goroutine in RunWithHeartbeat runs concurrently), then
	// advance the clock to fire it. Without waiting, Advance might fire before
	// the goroutine calls clk.NewTicker(5ms).
	staleJ.SetStale()
	testwait.External(t, "heartbeat-ticker-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)
	clk.Advance(testtime.D5ms)

	assertPhase2LeaseLost(t, memJ, inst.ID, driveErrCh)

	// Phase 3 — coordinator-2 re-claims the StatusCompensating instance and
	// drives recovery. c2 uses memJ directly so its Heartbeat always returns true.
	cfg2 := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
	c2, err := NewCoordinator(memJ, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(cfg2))
	if err != nil {
		t.Fatalf("NewCoordinator c2: %v", err)
	}

	clk.Advance(testtime.D60s + testtime.D1ms)
	claimed3, _, err := memJ.ClaimPending(context.Background(), 1, testtime.D60s)
	if err != nil || len(claimed3) == 0 {
		t.Fatalf("ClaimPending phase3: %v / %d", err, len(claimed3))
	}
	if err := c2.driveOne(context.Background(), claimed3[0]); err != nil {
		t.Fatalf("driveOne phase3 (recovery): %v", err)
	}

	assertPhase3RecoveryFinal(t, memJ, inst.ID, &step1CompensateCalls)
}

// assertPhase2LeaseLost verifies that driveOne returned nil on lease-lost and
// coordinator-1 did not write any terminal event. Also asserts the Phase 2
// pre-condition: instance in StatusCompensating with non-terminal last event.
func assertPhase2LeaseLost(t *testing.T, j *journal.MemJournal, instID idutil.SafeID, driveErrCh <-chan error) {
	t.Helper()
	select {
	case driveErr := <-driveErrCh:
		if driveErr != nil {
			t.Fatalf("driveOne phase2 should return nil on lease-lost, got: %v", driveErr)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("driveOne phase2 did not return within 2s after lease-lost")
	}

	evs, err := j.Load(context.Background(), instID)
	if err != nil {
		t.Fatalf("Load after phase2: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind.IsTerminal() {
			t.Fatalf("coordinator-1 wrote a terminal event after lease-lost: %s", ev.Kind)
		}
	}

	// Phase 2 invariant: instance is in StatusCompensating with no terminal event.
	// Asserting this before Phase 3 ClaimPending makes failure diagnosis friendlier.
	if len(evs) == 0 {
		t.Fatal("Phase 2 invariant check: no events in journal after coordinator-1 compensation walk")
	}
	if evs[len(evs)-1].Kind.IsTerminal() {
		t.Fatalf("Phase 2 invariant violated: last event is terminal %s before Phase 3 recovery", evs[len(evs)-1].Kind)
	}
}

// assertPhase3RecoveryFinal checks the final journal state after coordinator-2 recovery.
// coordinator-2's recovery: collectCommittedSteps filters step1 (already has
// KindStepCompensationFailed on the log from coordinator-1's partial attempt).
// The remaining compensation walk is empty — step1.Compensate is NOT re-run
// (idempotent: already attempted, not retried, #1181 F2). However,
// priorFailureCount > 0 so compensateErrors is seeded from history (#1181 F1):
// the recovery terminates with StatusCompensationFailed, not StatusCompensated,
// because the prior partial failure must not be silently discarded.
func assertPhase3RecoveryFinal(t *testing.T, j *journal.MemJournal, instID idutil.SafeID, step1CompensateCalls *atomic.Int64) {
	t.Helper()
	evsFinal, err := j.Load(context.Background(), instID)
	if err != nil {
		t.Fatalf("Load final: %v", err)
	}
	if len(evsFinal) == 0 {
		t.Fatal("no events in journal after recovery")
	}
	last := evsFinal[len(evsFinal)-1]
	if !last.Kind.IsTerminal() {
		t.Errorf("last event = %s, want terminal", last.Kind)
	}
	if last.Kind != journal.KindSagaCompensationFailed {
		t.Errorf("last event = %s, want saga_compensation_failed (prior failure preserved across recovery)", last.Kind)
	}
	// step1.Compensate called exactly once (coordinator-1's first attempt before
	// lease-lost; coordinator-2's recovery skips it because KindStepCompensationFailed
	// already appears in the journal).
	if got := step1CompensateCalls.Load(); got != 1 {
		t.Errorf("step1CompensateCalls = %d, want 1 (c1 attempted; c2 recovery skips already-attempted)", got)
	}
}
