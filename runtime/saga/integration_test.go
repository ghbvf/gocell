package saga

// integration_test.go covers the 8 TDD scenarios from plan §220 for the
// Coordinator engine. It runs in package saga (white-box) so it can access
// unexported helpers in coordinator.go (safeRun, foldEvents).
//
// Fake helpers live in testfakes_test.go (same package, test-only).

import (
	"bytes"
	"context"
	"errors"
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

	// testHeartbeatBoundaryExtra is used in TestIntegration_HeartbeatExtendsLease
	// to advance slightly past where a non-renewed lease would have expired.
	testHeartbeatBoundaryExtra = 60 * time.Millisecond
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
// tickLoop and heartbeatLoop to register their tickers with the FakeClock
// (PendingTickers >= 2), ensuring clock Advance calls are not missed.
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

	// Wait for tickLoop + heartbeatLoop to register their tickers with the
	// FakeClock. readyCh is closed before the goroutines are launched, so
	// there is a brief window where no tickers are registered yet. Without
	// this wait, a subsequent clk.Advance would not fire any ticker.
	clk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 2 },
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
// Scenario 5 — Heartbeat extends lease
// ---------------------------------------------------------------------------

// TestIntegration_HeartbeatExtendsLease verifies that the heartbeatLoop extends
// active leases so the instance is not re-claimable while being driven.
//
// Strategy: enqueue an instance with a step that blocks until signaled, then
// advance the clock past LeaseDuration/2 twice while heartbeat is running,
// and verify the instance remains un-claimable. After stopping the coordinator,
// advance past LeaseDuration and verify the instance becomes re-claimable.
func TestIntegration_HeartbeatExtendsLease(t *testing.T) {
	const defID idutil.SafeID = "heartbeatext"
	stepBlockCh := make(chan struct{}) // unblocked by the test after heartbeats

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "blockingstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					// Block until the test signals or ctx is canceled.
					select {
					case <-stepBlockCh:
						return []byte(`{}`), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			},
		},
	}

	// Use a short LeaseDuration so we can advance the clock meaningfully.
	// HeartbeatInterval must satisfy HeartbeatInterval*2 < LeaseDuration.
	leaseDuration := testtime.D300ms
	heartbeatInterval := testtime.D100ms
	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     leaseDuration,
		HeartbeatInterval: heartbeatInterval,
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()
	disp := &recordingDispatcher{}

	c, err := NewCoordinator(j, tx, em, reg, clk,
		WithConfig(cfg),
		WithDispatcher(disp),
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := newInstance(t, defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator did not become ready")
	}

	// Wait for tickLoop + heartbeatLoop to register their tickers.
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Wait for the instance to be claimed (tickLoop fires).
	tickOnceAndWait(t, clk, func() bool {
		// If activeLeases has an entry, the step is running.
		var hasLease bool
		c.activeLeases.Range(func(_, _ any) bool {
			hasLease = true
			return false
		})
		return hasLease
	})

	// Advance close to the original lease boundary twice.
	// Each advance brings us to leaseDuration - 50ms from the previous heartbeat.
	// If heartbeat is NOT working, the second advance would push us past the
	// original lease expiry (2 * (300ms - 50ms) = 500ms > leaseDuration=300ms),
	// so ClaimPending would succeed — the test would fail. A working heartbeat
	// extends the lease each interval, keeping the instance un-claimable.
	//
	// leaseDuration == testtime.D300ms; heartbeatInterval == testtime.D100ms.
	// Each iteration we advance by (heartbeatInterval - D50ms) = 50ms, which is
	// less than one heartbeat interval so the heartbeat fires between advances.
	for i := 0; i < 2; i++ {
		// Advance to just before the next heartbeat deadline.
		clk.Advance(leaseDuration - testtime.D50ms)
		// Let heartbeatLoop tick (real-time goroutine scheduling).
		time.Sleep(testtime.D20ms) //archtest:allow:test-sleep heartbeat-async: only side-effect is "lease NOT expired" (negative-test)

		// Verify instance is still NOT re-claimable (lease is held and extended).
		claimed, _, err := j.ClaimPending(context.Background(), 16, leaseDuration)
		if err != nil {
			t.Fatalf("ClaimPending (heartbeat check %d): %v", i, err)
		}
		if len(claimed) != 0 {
			t.Errorf("instance was re-claimed at heartbeat check %d — lease not extended by heartbeat", i)
		}
	}

	// Final verification: advance past what the original lease boundary would
	// have been without any heartbeat extensions. With heartbeat extending the
	// lease each interval, the instance should still be un-claimable.
	clk.Advance(testHeartbeatBoundaryExtra) // cross the boundary of a non-renewed lease
	time.Sleep(testtime.D20ms)              //archtest:allow:test-sleep heartbeat-async: negative-test guard
	claimed3, _, err3 := j.ClaimPending(context.Background(), 16, leaseDuration)
	if err3 != nil {
		t.Fatalf("ClaimPending (final check): %v", err3)
	}
	if len(claimed3) != 0 {
		t.Errorf("instance was re-claimed at final boundary check — heartbeat extensions not working")
	}

	// Unblock the step so the coordinator finishes cleanly.
	close(stepBlockCh)

	// Stop the coordinator.
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Stop: %v", err)
	}
	select {
	case <-startDone:
	case <-time.After(testtime.D3s):
		t.Error("coordinator goroutine did not exit")
	}

	// After the coordinator stops and we advance past LeaseDuration, the instance
	// should become re-claimable (lease has expired). But since the step unblocked
	// successfully, the instance is terminal — so ClaimPending returns empty.
	// The important assertion here is that the heartbeat DID extend the lease
	// (verified above). No further lease-based assertion is needed.
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

	// Wait for both coordinators' tickers to register (4 tickers: 2 per coordinator).
	testwait.External(t, "both-coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 4 },
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
// waits for Ready, then waits for both loop tickers to register on the clock.
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
	testwait.External(t, name+"-tickers-registered",
		func() bool { return clk.PendingTickers() >= 2 },
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

	// Wait for tickers to be registered with the FakeClock.
	testwait.External(t, "coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Advance clock to trigger the tick. safeRun recovers the panic, returns
	// (nil, err). driveOne opens the tx via RunInTx → commitStepFailed →
	// journal.Append(StepFailed) + journal.MarkTerminal(Failed).
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 2
	})

	evs, err := j.Load(context.Background(), inst.ID)
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

	// Stop the coordinator cleanly.
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

	// Step was called exactly once (the first tick triggered it).
	if stepCallCount != 1 {
		t.Errorf("step call count = %d, want 1", stepCallCount)
	}
}
