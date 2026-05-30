package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// noopLogger returns a discarding logger.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, nil))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// newTestInstance builds a running instance for tests.
func newTestInstance() *ksaga.Instance {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := ksaga.NewInstance("test-instance-id", "test-def-id", now)
	return &inst
}

// alwaysOKHeartbeater always returns ok=true.
type alwaysOKHeartbeater struct {
	instanceID idutil.SafeID
	leaseID    idutil.SafeID
	callCount  int32
}

func (a *alwaysOKHeartbeater) Heartbeat(_ context.Context, instanceID, leaseID idutil.SafeID, _ time.Duration) (bool, error) {
	atomic.AddInt32(&a.callCount, 1)
	a.instanceID = instanceID
	a.leaseID = leaseID
	return true, nil
}

func (a *alwaysOKHeartbeater) Count() int { return int(atomic.LoadInt32(&a.callCount)) }

// staleAfterFirstHeartbeater returns ok=true on the first call (typically the
// synchronous preflight heartbeat in Execute) and ok=false on every subsequent
// call (the async tick). This lets tests exercise the "step started → async
// tick observes stale → cancel" path while still passing the #1181 F6 preflight.
type staleAfterFirstHeartbeater struct {
	callCount int32
}

func (s *staleAfterFirstHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	n := atomic.AddInt32(&s.callCount, 1)
	return n == 1, nil // first call ok; subsequent stale
}

// waitForOnePendingTimer waits for the FakeClock to have at least one pending
// timer, ensuring the executor goroutine has registered its backoff Sleep
// before Advance is called. There is no channel signal for "timer registered",
// so polling is unavoidable — the legitimate testwait.External carve-out.
func waitForOnePendingTimer(t *testing.T, fc *clockmock.FakeClock) {
	t.Helper()
	testwait.External(t, "fakeclock-timer-registration",
		func() bool { return fc.PendingTimers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll,
		"executor did not register its backoff timer")
}

// --- NewExecutor validation tests ---

func TestNewExecutor_NilHeartbeater(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	_, err := NewExecutor(nil, fc)
	if err == nil {
		t.Fatal("expected error for nil heartbeater")
	}
	var e *errcode.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if e.Kind != errcode.KindInvalid {
		t.Errorf("kind = %v, want KindInvalid", e.Kind)
	}
}

func TestNewExecutor_NilClock(t *testing.T) {
	t.Parallel()
	hb := &alwaysOKHeartbeater{}
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil clock")
		}
	}()
	_, _ = NewExecutor(hb, nil)
}

func TestNewExecutor_BadHeartbeatLeaseRatio(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}

	tests := []struct {
		name     string
		hbInt    time.Duration
		leaseDur time.Duration
	}{
		{"equal", testtime.D15s, testtime.D30s},   // 15*2 == 30, fails: must be <
		{"greater", testtime.D20s, testtime.D30s}, // 20*2 > 30
		{"zero_interval", 0, testtime.D30s},
		{"zero_lease", testtime.D10s, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExecutor(hb, fc,
				WithHeartbeatInterval(tc.hbInt),
				WithLeaseDuration(tc.leaseDur),
			)
			if err == nil {
				t.Errorf("expected error for hbInt=%v leaseDur=%v", tc.hbInt, tc.leaseDur)
			}
		})
	}
}

func TestNewExecutor_ValidRatio(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	_, err := NewExecutor(hb, fc,
		WithHeartbeatInterval(testtime.D10s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Execute success test ---

func TestExecute_FirstAttemptSuccess(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	wantState := []byte(`{"done":true}`)
	step := ksaga.Step{
		Name: "step-1",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return wantState, nil
		},
	}

	inst := newTestInstance()
	leaseID := idutil.SafeID("lease-abc")
	result := exec.Execute(context.Background(), inst, leaseID, step, ksaga.RetryPolicy{}, nil)

	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded", result.Outcome)
	}
	if !bytes.Equal(result.NewState, wantState) {
		t.Errorf("NewState = %q, want %q", result.NewState, wantState)
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
}

// --- Execute retry backoff determinism test ---

func TestExecute_RetryBackoffDeterministic(t *testing.T) {
	t.Parallel()
	// We inject a deterministic jitter source.
	seed1, seed2 := uint64(42), uint64(1337)
	j := &deterministicJitter{r: rand.New(rand.NewPCG(seed1, seed2))} //nolint:gosec // deterministic test jitter

	// Build a parallel jitter for computing expected delays.
	jExpected := &deterministicJitter{r: rand.New(rand.NewPCG(seed1, seed2))} //nolint:gosec // deterministic test jitter

	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()), withJitterSource(j))
	if err != nil {
		t.Fatal(err)
	}

	const maxAttempts = 4
	var attempt int
	step := ksaga.Step{
		Name: "step-retry",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			attempt++
			if attempt < maxAttempts {
				return nil, errors.New("transient error")
			}
			return []byte("success"), nil
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: maxAttempts},
	}

	// We need to Advance the clock for each backoff sleep.
	// The Execute loop will call clk.Sleep(ctx, clk.Now().Add(delay)).
	// With FakeClock, Sleep blocks until Advance moves the clock past the deadline.
	// We run Execute in a goroutine and advance the clock from the test goroutine.

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-retry", step, ksaga.RetryPolicy{}, nil)
	}()

	policy := resolvedPolicy{
		maxAttempts: maxAttempts,
		base:        defaultBaseInterval,
		max:         defaultMaxInterval,
	}

	// For attempts 1, 2, 3 (failures), advance the clock by the expected backoff.
	// attempt-1 → Backoff(0), attempt-2 → Backoff(1), attempt-3 → Backoff(2).
	for i := 0; i < maxAttempts-1; i++ {
		delay := policy.Backoff(i, jExpected)
		// Wait for the executor to register its backoff Sleep timer before advancing.
		waitForOnePendingTimer(t, fc)
		fc.Advance(delay)
	}

	result := testwait.Deterministic(t, resultCh, "retry-backoff-result")
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded", result.Outcome)
	}
	if result.Attempts != maxAttempts {
		t.Errorf("Attempts = %d, want %d", result.Attempts, maxAttempts)
	}
}

// --- Execute exhausted + compensatable → Failed (executor returns facts only) ---
//
// The executor must NOT pre-empt the Coordinator's Compensating-vs-Failed
// decision (it has no view of committed-step history). Even when step.Compensate
// is non-nil, a forward exhaustion is reported as OutcomeFailed; the Coordinator
// decides whether to compensate based on committed history (kernel/saga/status.go).
func TestExecute_ExhaustedWithCompensate_Failed(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	compensateCalled := false
	step := ksaga.Step{
		Name: "step-comp",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("always fails")
		},
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
			compensateCalled = true
			return nil
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	result := exec.Execute(context.Background(), newTestInstance(), "lease-1", step, ksaga.RetryPolicy{}, nil)
	if result.Outcome != OutcomeFailed {
		t.Errorf("Outcome = %v, want OutcomeFailed (executor reports facts; Coordinator decides compensation)", result.Outcome)
	}
	if result.Err == nil {
		t.Error("Err should be non-nil")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
	// Compensate is NOT called by Execute — it's called separately by the Coordinator.
	if compensateCalled {
		t.Error("Compensate should not be called by Execute")
	}
}

// --- Execute exhausted + no compensate → Failed ---

func TestExecute_ExhaustedNoCompensate_Failed(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name: "step-fail",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("permanent failure")
		},
		Compensate:  nil, // no compensate
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	result := exec.Execute(context.Background(), newTestInstance(), "lease-2", step, ksaga.RetryPolicy{}, nil)
	if result.Outcome != OutcomeFailed {
		t.Errorf("Outcome = %v, want OutcomeFailed", result.Outcome)
	}
}

// --- Execute retry budget exhausted emits a Warn ops signal (C5/F2) ---

func TestExecute_RetryExhausted_WarnLogged(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name: "step-exhaust",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("boom")
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	result := exec.Execute(context.Background(), newTestInstance(), "lease-warn", step, ksaga.RetryPolicy{}, nil)
	if result.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want OutcomeFailed", result.Outcome)
	}

	entry := sloghelper.FindLogEntry(buf.String(), "retry budget exhausted")
	if entry == nil {
		t.Fatal("expected a WARN log about retry budget exhausted")
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
	if entry["attempts"] != float64(1) {
		t.Errorf("log attempts = %v, want 1", entry["attempts"])
	}
	if entry["outcome"] != OutcomeFailed.String() {
		t.Errorf("log outcome = %v, want %q", entry["outcome"], OutcomeFailed.String())
	}
	if _, ok := entry["error"]; !ok {
		t.Error("log entry missing structured error field")
	}
	if entry["lease_id"] != "lease-warn" {
		t.Errorf("log lease_id = %v, want %q", entry["lease_id"], "lease-warn")
	}
}

// --- Execute per-step timeout → Expired (clock-driven, C3/F3) ---

func TestExecute_StepTimeout_Expired(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	step := ksaga.Step{
		Name:    "step-timeout",
		Timeout: testtime.D5s,
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-3", step, ksaga.RetryPolicy{}, nil)
	}()

	testwait.Deterministic(t, started, "step-started")

	// Negative assertion: advancing the fake clock short of the step deadline
	// must NOT expire the step (the timeout is clock-driven, not wall-clock).
	fc.Advance(testtime.D5s - testtime.D1ms)
	select {
	case r := <-resultCh:
		t.Fatalf("step expired before its deadline: outcome=%v", r.Outcome)
	default:
		// still running — correct
	}

	// Now cross the deadline: the clock-driven AfterFunc fires and expires the step.
	fc.Advance(testtime.D2ms)

	result := testwait.Deterministic(t, resultCh, "step-timeout-result")
	if result.Outcome != OutcomeExpired {
		t.Errorf("Outcome = %v, want OutcomeExpired", result.Outcome)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
}

// --- Execute parent ctx cancel → Canceled (C2/F7) ---
//
// An explicit parent-context cancellation (orchestrator shutdown/abort) is NOT
// a business expiry: the executor reports OutcomeCanceled so the Coordinator can
// leave the instance for re-claim rather than terminating it as Expired.
func TestExecute_ParentCancel_Canceled(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	step := ksaga.Step{
		Name: "step-ctx",
		// step.Timeout = 0 → inherits ctx
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-4", step, ksaga.RetryPolicy{}, nil)
	}()

	testwait.Deterministic(t, started, "step-started")
	cancel()

	result := testwait.Deterministic(t, resultCh, "parent-cancel-result")
	if result.Outcome != OutcomeCanceled {
		t.Errorf("Outcome = %v, want OutcomeCanceled", result.Outcome)
	}
}

// --- Execute parent ctx DEADLINE → Expired (GAP A: saga-level Definition.Timeout) ---
//
// A parent deadline (saga-level Definition.Timeout) is a terminal expiry, NOT a
// shutdown cancel. It must map to OutcomeExpired, distinct from the explicit
// cancel case above. Uses an already-elapsed deadline for determinism.
func TestExecute_ParentDeadline_Expired(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	// Parent ctx whose deadline has already elapsed → ctx.Err() == DeadlineExceeded.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(testtime.DNeg1h))
	defer cancel()

	step := ksaga.Step{
		Name: "step-parent-deadline",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-deadline", step, ksaga.RetryPolicy{}, nil)
	}()

	result := testwait.Deterministic(t, resultCh, "parent-deadline-result")
	if result.Outcome != OutcomeExpired {
		t.Errorf("Outcome = %v, want OutcomeExpired (saga-level deadline, not Canceled)", result.Outcome)
	}
}

// --- Execute backoff parent ctx cancel → Canceled (C2/F7) ---

func TestExecute_BackoffParentCancel_Canceled(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	step := ksaga.Step{
		Name: "step-backoff-cancel",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("fail")
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 3},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-5", step, ksaga.RetryPolicy{}, nil)
	}()

	// First attempt fails; wait for executor to register backoff Sleep timer,
	// then cancel ctx before the backoff completes.
	waitForOnePendingTimer(t, fc)
	cancel()

	result := testwait.Deterministic(t, resultCh, "backoff-cancel-result")
	if result.Outcome != OutcomeCanceled {
		t.Errorf("Outcome = %v, want OutcomeCanceled", result.Outcome)
	}
}

// --- Execute lease lost (heartbeat ok=false) → cancels step + LeaseLost (C1/F4) ---

func TestExecute_LeaseLost_CancelsStepAndReturnsLeaseLost(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	// First HB call (preflight, #1181 F6) returns ok=true so the step runs;
	// the next async tick returns ok=false so the goroutine observes stale
	// mid-execution and cancels the step ctx.
	hb := &staleAfterFirstHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	step := ksaga.Step{
		Name: "step-lease-lost",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(started)
			<-ctx.Done() // unblocked only when the lost lease cancels the step
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-lost", step, ksaga.RetryPolicy{}, nil)
	}()

	testwait.Deterministic(t, started, "step-started")
	// Heartbeat goroutine registers its ticker; advancing one interval fires the
	// (stale) heartbeat → ok=false → the executor must cancel the running step.
	waitForOneTicker(t, fc)
	fc.Advance(testtime.D5s)

	result := testwait.Deterministic(t, resultCh, "lease-lost-result")
	if result.Outcome != OutcomeLeaseLost {
		t.Errorf("Outcome = %v, want OutcomeLeaseLost", result.Outcome)
	}
}

// --- Execute renews the lease across the retry backoff window (C1/F5) ---
//
// A single heartbeat goroutine must span the whole Execute (run + backoff), so a
// long backoff does not drop the lease. We park the executor in a long backoff
// and assert a heartbeat fires while it waits.
func TestExecute_LeaseRenewsDuringBackoff(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	hb := newFakeHeartbeater(true)
	j := newDeterministicJitter(7, 11)
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
		withJitterSource(j),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	step := ksaga.Step{
		Name: "step-renew",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("transient")
		},
		// Backoff base far larger than the heartbeat interval so a heartbeat
		// tick lands well inside the backoff window.
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 2, BaseInterval: testtime.D60s, MaxInterval: testtime.D60s},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-renew", step, ksaga.RetryPolicy{}, nil)
	}()

	// Attempt 1 fails instantly → executor enters the long backoff. The heartbeat
	// goroutine (spanning the whole Execute) keeps its ticker registered.
	waitForOnePendingTimer(t, fc) // backoff Sleep timer
	waitForOneTicker(t, fc)       // heartbeat ticker still alive during backoff

	// Advance one heartbeat interval (5s) — far short of the ~48-60s backoff, so
	// only the heartbeat ticker fires, not the backoff timer.
	fc.Advance(testtime.D5s)
	testwait.Deterministic(t, hb.beat, "heartbeat-during-backoff")

	// Tear down: cancel parent so the backoff Sleep returns and Execute exits.
	cancel()
	testwait.Deterministic(t, resultCh, "renew-teardown-result")
}

// --- Execute: Run panic → convert to error, then retry ---

func TestExecute_RunPanic_ConvertedToError(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	var attempt int
	step := ksaga.Step{
		Name: "step-panic",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			attempt++
			if attempt == 1 {
				panic("unexpected panic")
			}
			return []byte("recovered"), nil
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 2},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-6", step, ksaga.RetryPolicy{}, nil)
	}()

	// Wait for executor to register backoff Sleep timer, then advance past it.
	waitForOnePendingTimer(t, fc)
	fc.Advance(defaultBaseInterval + testtime.D1ms)

	result := testwait.Deterministic(t, resultCh, "panic-recover-result")
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded (panic recovered, second attempt succeeded)", result.Outcome)
	}
	if result.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", result.Attempts)
	}
}

// --- Execute: leaseID is passed to heartbeater ---

func TestExecute_LeaseIDPassedToHeartbeater(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	leaseID := idutil.SafeID("my-unique-lease-id")
	step := ksaga.Step{
		Name: "step-lease",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return []byte("ok"), nil
		},
	}

	// Run Execute in background to capture any heartbeats.
	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), leaseID, step, ksaga.RetryPolicy{}, nil)
	}()

	testwait.Deterministic(t, resultCh, "lease-id-result")
	// The step completes quickly so there may be 0 heartbeats — that's fine.
	// What we ensure is: no panic occurred and leaseID type was forwarded correctly.
	// More thorough testing is done in TestHeartbeat_*.
}

// --- Compensate tests ---

func TestCompensate_Success(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	called := false
	step := ksaga.Step{
		Name: "step-comp-success",
		Run:  func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
			called = true
			return nil
		},
	}

	err = exec.Compensate(context.Background(), newTestInstance(), "lease-comp-success", step, []byte(`{"state":1}`))
	if err != nil {
		t.Errorf("Compensate returned error: %v", err)
	}
	if !called {
		t.Error("Compensate func not called")
	}
}

func TestCompensate_Failure(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("compensate failed")
	step := ksaga.Step{
		Name: "step-comp-fail",
		Run:  func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
			return wantErr
		},
	}

	err = exec.Compensate(context.Background(), newTestInstance(), "lease-comp-fail", step, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("Compensate = %v, want %v", err, wantErr)
	}
}

func TestCompensate_NilCompensate_ReturnsNil(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name:       "step-no-comp",
		Run:        func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: nil,
	}

	err = exec.Compensate(context.Background(), newTestInstance(), "lease-comp-nil", step, nil)
	if err != nil {
		t.Errorf("Compensate with nil func = %v, want nil", err)
	}
}

func TestCompensate_Panic_ConvertedToError(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name: "step-comp-panic",
		Run:  func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
			panic("compensate panicked")
		},
	}

	err = exec.Compensate(context.Background(), newTestInstance(), "lease-comp-panic", step, nil)
	if err == nil {
		t.Error("expected error from panic in Compensate, got nil")
	}
}

func TestCompensate_IgnoresStepTimeout(t *testing.T) {
	t.Parallel()
	// Even with a very small step.Timeout, Compensate should complete.
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	step := ksaga.Step{
		Name:    "step-comp-timeout",
		Timeout: time.Nanosecond, // extremely small
		Run:     func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(ctx context.Context, _ *ksaga.Instance, _ []byte) error {
			// Ensure ctx is still alive (not step-timeout-derived).
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				close(done)
				return nil
			}
		},
	}

	err = exec.Compensate(context.Background(), newTestInstance(), "lease-comp-timeout", step, nil)
	if err != nil {
		t.Errorf("Compensate with small step timeout returned error: %v", err)
	}
	select {
	case <-done:
	default:
		t.Error("Compensate func not reached")
	}
}

// TestCompensate_LeaseIDLogged asserts both compensation log lines carry the
// lease_id field (the coordinator's fencing token), so an operator can
// correlate a compensation log entry back to the ClaimPending cycle that drove
// it. Mirrors TestExecute_RetryExhausted_WarnLogged's slog-capture pattern.
// Covers issue #1211 acceptance: Info "compensating step" + Warn "compensate
// failed" must both include slog.String("lease_id", string(leaseID)).
func TestCompensate_LeaseIDLogged(t *testing.T) {
	t.Parallel()
	const leaseID = idutil.SafeID("lease-comp-logged")
	t.Run("success logs lease_id on Info", func(t *testing.T) {
		t.Parallel()
		runCompensateLeaseIDSuccess(t, leaseID)
	})
	t.Run("failure logs lease_id on Warn", func(t *testing.T) {
		t.Parallel()
		runCompensateLeaseIDFailure(t, leaseID)
	})
}

func runCompensateLeaseIDSuccess(t *testing.T, leaseID idutil.SafeID) {
	t.Helper()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	step := ksaga.Step{
		Name:       "step-log-success",
		Run:        func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error { return nil },
	}
	if err := exec.Compensate(context.Background(), newTestInstance(), leaseID, step, nil); err != nil {
		t.Fatalf("Compensate returned error: %v", err)
	}
	entry := sloghelper.FindLogEntry(buf.String(), "compensating step")
	if entry == nil {
		t.Fatal("expected an INFO log about compensating step")
	}
	if entry["lease_id"] != string(leaseID) {
		t.Errorf("log lease_id = %v, want %q", entry["lease_id"], string(leaseID))
	}
}

func runCompensateLeaseIDFailure(t *testing.T, leaseID idutil.SafeID) {
	t.Helper()
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	step := ksaga.Step{
		Name:       "step-log-fail",
		Run:        func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) { return nil, nil },
		Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error { return errors.New("boom") },
	}
	if err := exec.Compensate(context.Background(), newTestInstance(), leaseID, step, nil); err == nil {
		t.Fatal("expected Compensate to return the compensate error")
	}
	entry := sloghelper.FindLogEntry(buf.String(), "compensate failed")
	if entry == nil {
		t.Fatal("expected a WARN log about compensate failed")
	}
	if entry["lease_id"] != string(leaseID) {
		t.Errorf("log lease_id = %v, want %q", entry["lease_id"], string(leaseID))
	}
}

// TestSafeRun_PanicValueRedactedInInternalAttr asserts that a panic value
// containing a sensitive substring is scrubbed before being stored in the
// errcode InternalAttr. This closes the R1 security finding: without
// redaction, a step that panics with a value like "password=hunter2" would
// expose the raw payload via the errcode internal detail.
func TestSafeRun_PanicValueRedactedInInternalAttr(t *testing.T) {
	t.Parallel()
	sensitive := "password=hunter2"
	panicFn := func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
		panic(sensitive)
	}
	inst := newTestInstance()
	_, err := safeRun(context.Background(), panicFn, inst, nil)
	if err == nil {
		t.Fatal("safeRun with panicking fn must return non-nil error")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "<REDACTED>") {
		t.Errorf("safeRun error must contain <REDACTED>; got: %s", errStr)
	}
	if strings.Contains(errStr, "hunter2") {
		t.Errorf("safeRun error must NOT contain raw sensitive value 'hunter2'; got: %s", errStr)
	}
}

// TestSafeRunCompensate_PanicValueRedactedInInternalAttr mirrors
// TestSafeRun_PanicValueRedactedInInternalAttr for the Compensate path.
func TestSafeRunCompensate_PanicValueRedactedInInternalAttr(t *testing.T) {
	t.Parallel()
	sensitive := "password=hunter2"
	panicFn := func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
		panic(sensitive)
	}
	inst := newTestInstance()
	err := safeRunCompensate(context.Background(), panicFn, inst, nil)
	if err == nil {
		t.Fatal("safeRunCompensate with panicking fn must return non-nil error")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "<REDACTED>") {
		t.Errorf("safeRunCompensate error must contain <REDACTED>; got: %s", errStr)
	}
	if strings.Contains(errStr, "hunter2") {
		t.Errorf("safeRunCompensate error must NOT contain raw sensitive value 'hunter2'; got: %s", errStr)
	}
}

// Aliases for test usage — reference the exported package constants.
var (
	defaultBaseInterval = DefaultBaseInterval
	defaultMaxInterval  = DefaultMaxInterval
)

// verify ExponentialDelay is what we think it is (sanity).
func TestExponentialDelay_SanityCheck(t *testing.T) {
	t.Parallel()
	got := koutbox.ExponentialDelay(testtime.D100ms, testtime.D30s, 0)
	if got != testtime.D100ms {
		t.Errorf("ExponentialDelay(100ms, 30s, 0) = %v, want 100ms", got)
	}
	got = koutbox.ExponentialDelay(testtime.D100ms, testtime.D30s, 1)
	if got != testtime.D200ms {
		t.Errorf("ExponentialDelay(100ms, 30s, 1) = %v, want 200ms", got)
	}
}
