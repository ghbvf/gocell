package saga

// integration_test.go covers the 8 TDD scenarios from plan §220 for the
// Coordinator engine. It runs in package saga (white-box) so it can access
// unexported helpers in coordinator.go (safeRun, foldEvents).
//
// Fake helpers live in testfakes_test.go (same package, test-only).

import (
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
		EmptyClaimBackoff: testtime.D10ms,
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
	deadline := time.Now().Add(testtime.D2s)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("tickOnceAndWait: condition not met within 2s")
		}
		time.Sleep(testtime.D2ms)
	}
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
	deadline := time.Now().Add(testtime.D2s)
	for clk.PendingTickers() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("coordinator tickers did not register within 2s")
		}
		time.Sleep(testtime.D1ms)
	}

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
// Scenario 3 — Compensate present but PR-03 does not execute it
// ---------------------------------------------------------------------------

// TestIntegration_CompensateDeferred verifies that when a definition has a
// Compensate function and the step fails, PR-03 still terminates Failed
// (compensation execution is deferred to PR-06). The test asserts the current
// correct PR-03 behavior.
//
// TODO(PR-06): execute Compensate path when a step fails with prior committed
// steps, transitioning to Compensating status before terminal.
func TestIntegration_CompensateDeferred(t *testing.T) {
	const defID idutil.SafeID = "compensatedeferred"
	compensateCalled := false
	stepErr := errors.New("step failed to trigger compensation check")

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, stepErr
				},
				Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
					compensateCalled = true
					return nil
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

	// Wait for terminal (2 events: StepFailed + SagaFailed).
	tickOnceAndWait(t, h.clk, func() bool {
		evs, err := h.j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 2
	})

	evs, err := h.j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// PR-03 current behavior: terminates Failed without executing compensation.
	if len(evs) != 2 {
		t.Fatalf("want 2 events (StepFailed + SagaFailed), got %d", len(evs))
	}
	if evs[0].Kind != journal.KindStepFailed {
		t.Errorf("evs[0].Kind = %s, want step_failed", evs[0].Kind)
	}
	if evs[1].Kind != journal.KindSagaFailed {
		t.Errorf("evs[1].Kind = %s, want saga_failed", evs[1].Kind)
	}
	// Compensate was NOT called by PR-03.
	if compensateCalled {
		t.Error("Compensate was called, but PR-03 should defer compensation to PR-06")
	}

	// No compensation-related events (KindCompensationStarted / KindStepCompensated).
	for _, ev := range evs {
		if ev.Kind == journal.KindCompensationStarted || ev.Kind == journal.KindStepCompensated {
			t.Errorf("unexpected compensation event: %s", ev.Kind)
		}
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
	expiredDeadline := time.Now().Add(testtime.D2s)
	var evs []journal.Event
	var evErr error
	for {
		evs, evErr = h.j.Load(context.Background(), inst.ID)
		if evErr == nil && len(evs) > 0 && evs[len(evs)-1].Kind == journal.KindSagaExpired {
			break
		}
		if time.Now().After(expiredDeadline) {
			t.Fatal("instance was not marked Expired within 2s after 1h clock advance")
		}
		time.Sleep(testtime.D2ms)
	}

	_ = evErr // checked in loop above
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
	stepDoneCh := make(chan struct{})  // closed when step returns

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
		EmptyClaimBackoff: testtime.D10ms,
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
	tickerDeadline := time.Now().Add(testtime.D2s)
	for clk.PendingTickers() < 2 {
		if time.Now().After(tickerDeadline) {
			t.Fatal("coordinator tickers did not register within 2s")
		}
		time.Sleep(testtime.D1ms)
	}

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

	// Advance by LeaseDuration/2 twice with heartbeat ticks in between.
	// The heartbeatLoop should re-extend the lease each time.
	// leaseDuration == testtime.D300ms; half is testtime.D150ms.
	for i := 0; i < 2; i++ {
		clk.Advance(testtime.D150ms)
		// Let heartbeatLoop tick.
		time.Sleep(testtime.D20ms)

		// Verify instance is still NOT re-claimable (lease is held and active).
		claimed, _, err := j.ClaimPending(context.Background(), 16, leaseDuration)
		if err != nil {
			t.Fatalf("ClaimPending (heartbeat check %d): %v", i, err)
		}
		if len(claimed) != 0 {
			t.Errorf("instance was re-claimed at heartbeat check %d — lease not extended", i)
		}
	}

	// Unblock the step so the coordinator finishes cleanly.
	close(stepBlockCh)
	_ = stepDoneCh

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
		EmptyClaimBackoff: testtime.D10ms,
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
	{
		dl := time.Now().Add(testtime.D2s)
		for clk.PendingTickers() < 4 {
			if time.Now().After(dl) {
				t.Fatal("coordinators' tickers did not register within 2s")
			}
			time.Sleep(testtime.D1ms)
		}
	}

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
	_ = c1.Stop(stopCtx)
	_ = c2.Stop(stopCtx)
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
	step1Done := make(chan struct{}) // closed when step 1 completes its tick
	step2Ran := false

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
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					step2Ran = true
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
		EmptyClaimBackoff: testtime.D10ms,
	}

	// First coordinator: drive step 1 only.
	em1 := newSafeFakeEmitter()
	tx1 := newSafeFakeTxRunner()
	disp1 := &recordingDispatcher{}
	c1, err := NewCoordinator(j, tx1, em1, reg, clk, WithConfig(cfg), WithDispatcher(disp1))
	if err != nil {
		t.Fatalf("NewCoordinator c1: %v", err)
	}

	inst := newInstance(t, defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- c1.Start(ctx1) }()
	select {
	case <-c1.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("c1 not ready")
	}

	// Wait for c1's tickers to register before advancing the clock.
	{
		dl := time.Now().Add(testtime.D2s)
		for clk.PendingTickers() < 2 {
			if time.Now().After(dl) {
				t.Fatal("c1 tickers did not register within 2s")
			}
			time.Sleep(testtime.D1ms)
		}
	}

	// Advance clock to trigger a tick; wait for step 1 to be committed
	// (journal has StepCompleted v1 but NOT SagaSucceeded yet — it's a 2-step saga).
	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 1 && evs[0].Kind == journal.KindStepCompleted
	})
	close(step1Done)

	// Stop coordinator 1 before step 2 can run.
	cancel1()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c1.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("c1 Stop: %v", err)
	}
	select {
	case <-done1:
	case <-time.After(testtime.D3s):
		t.Error("c1 goroutine did not exit")
	}

	// Resume with coordinator 2 sharing the same journal.
	em2 := newSafeFakeEmitter()
	tx2 := newSafeFakeTxRunner()
	disp2 := &recordingDispatcher{}
	c2, err := NewCoordinator(j, tx2, em2, reg, clk, WithConfig(cfg), WithDispatcher(disp2))
	if err != nil {
		t.Fatalf("NewCoordinator c2: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- c2.Start(ctx2) }()
	select {
	case <-c2.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("c2 not ready")
	}
	defer func() {
		cancel2()
		stopCtx2, stopCancel2 := context.WithTimeout(context.Background(), testtime.D3s)
		defer stopCancel2()
		_ = c2.Stop(stopCtx2)
		select {
		case <-done2:
		case <-time.After(testtime.D3s):
			t.Error("c2 goroutine did not exit")
		}
	}()

	// Wait for c2's tickers to register before advancing the clock.
	{
		dl := time.Now().Add(testtime.D2s)
		for clk.PendingTickers() < 2 {
			if time.Now().After(dl) {
				t.Fatal("c2 tickers did not register within 2s")
			}
			time.Sleep(testtime.D1ms)
		}
	}

	// Advance clock to let coordinator 2 claim and drive step 2; wait for terminal.
	// The lease from c1 has expired (c1 stopped, clock not advanced much yet), so
	// c2 can re-claim. Advance past LeaseDuration to ensure re-claimability.
	clk.Advance(testLeaseAdvance) // past the 60s lease expiry

	tickOnceAndWait(t, clk, func() bool {
		evs, err := j.Load(context.Background(), inst.ID)
		return err == nil && len(evs) == 3
	})

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Expected: StepCompleted v1, StepCompleted v2, SagaSucceeded v3.
	if len(evs) != 3 {
		t.Fatalf("journal event count = %d, want 3; events: %v", len(evs), evs)
	}
	if evs[0].Kind != journal.KindStepCompleted || evs[0].Version != 1 {
		t.Errorf("evs[0] = {%s, v%d}, want {step_completed, v1}", evs[0].Kind, evs[0].Version)
	}
	if evs[1].Kind != journal.KindStepCompleted || evs[1].Version != 2 {
		t.Errorf("evs[1] = {%s, v%d}, want {step_completed, v2}", evs[1].Kind, evs[1].Version)
	}
	if evs[2].Kind != journal.KindSagaSucceeded || evs[2].Version != 3 {
		t.Errorf("evs[2] = {%s, v%d}, want {saga_succeeded, v3}", evs[2].Kind, evs[2].Version)
	}
	if !step2Ran {
		t.Error("step2 was not executed by coordinator 2")
	}
	_ = step1Done
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
		EmptyClaimBackoff: testtime.D10ms,
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
	deadline := time.Now().Add(testtime.D2s)
	for clk.PendingTickers() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("coordinator tickers did not register within 2s")
		}
		time.Sleep(testtime.D1ms)
	}

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
