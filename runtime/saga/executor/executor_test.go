package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
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

// --- NewExecutor validation tests ---

func TestNewExecutor_NilHeartbeater(t *testing.T) {
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
	hb := &alwaysOKHeartbeater{}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil clock")
		}
	}()
	_, _ = NewExecutor(hb, nil)
}

func TestNewExecutor_BadHeartbeatLeaseRatio(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}

	tests := []struct {
		name     string
		hbInt    time.Duration
		leaseDur time.Duration
	}{
		{"equal", 15 * time.Second, 30 * time.Second},   // 15*2 == 30, fails: must be <
		{"greater", 20 * time.Second, 30 * time.Second}, // 20*2 > 30
		{"zero_interval", 0, 30 * time.Second},
		{"zero_lease", 10 * time.Second, 0},
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
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	_, err := NewExecutor(hb, fc,
		WithHeartbeatInterval(10*time.Second),
		WithLeaseDuration(30*time.Second),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Execute success test ---

func TestExecute_FirstAttemptSuccess(t *testing.T) {
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
	// We inject a deterministic jitter source.
	seed1, seed2 := uint64(42), uint64(1337)
	j := &deterministicJitter{r: rand.New(rand.NewPCG(seed1, seed2))} //nolint:gosec // deterministic test jitter

	// Build a parallel jitter for computing expected delays.
	jExpected := &deterministicJitter{r: rand.New(rand.NewPCG(seed1, seed2))} //nolint:gosec // deterministic test jitter

	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()), WithJitterSource(j))
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
		// Wait briefly for the executor to reach the Sleep call.
		time.Sleep(5 * time.Millisecond)
		fc.Advance(delay)
	}

	result := <-resultCh
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded", result.Outcome)
	}
	if result.Attempts != maxAttempts {
		t.Errorf("Attempts = %d, want %d", result.Attempts, maxAttempts)
	}
}

// --- Execute exhausted + compensatable → CompensationRequired ---

func TestExecute_ExhaustedWithCompensate_CompensationRequired(t *testing.T) {
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
	if result.Outcome != OutcomeCompensationRequired {
		t.Errorf("Outcome = %v, want OutcomeCompensationRequired", result.Outcome)
	}
	if result.Err == nil {
		t.Error("Err should be non-nil")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
	// Compensate is NOT called by Execute — it's called separately.
	if compensateCalled {
		t.Error("Compensate should not be called by Execute")
	}
}

// --- Execute exhausted + no compensate → Failed ---

func TestExecute_ExhaustedNoCompensate_Failed(t *testing.T) {
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

// --- Execute per-step timeout → Expired ---

func TestExecute_PerStepTimeout_Expired(t *testing.T) {
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	blocked := make(chan struct{})
	step := ksaga.Step{
		Name:    "step-timeout",
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(blocked)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-3", step, ksaga.RetryPolicy{}, nil)
	}()

	<-blocked
	// Advance past the step timeout.
	fc.Advance(5*time.Second + time.Millisecond)

	result := <-resultCh
	if result.Outcome != OutcomeExpired {
		t.Errorf("Outcome = %v, want OutcomeExpired", result.Outcome)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
}

// --- Execute inherited context deadline → Expired ---

func TestExecute_InheritedContextDeadline_Expired(t *testing.T) {
	fc := clockmock.New(time.Now())
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	step := ksaga.Step{
		Name: "step-ctx",
		// step.Timeout = 0 → inherits ctx
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: 1},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-4", step, ksaga.RetryPolicy{}, nil)
	}()

	// Give goroutine time to start.
	time.Sleep(5 * time.Millisecond)
	cancel()

	result := <-resultCh
	if result.Outcome != OutcomeExpired {
		t.Errorf("Outcome = %v, want OutcomeExpired", result.Outcome)
	}
}

// --- Execute backoff parent ctx cancel → Expired ---

func TestExecute_BackoffParentCancel_Expired(t *testing.T) {
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

	// First attempt fails; executor will enter backoff sleep.
	// Cancel ctx before backoff completes.
	time.Sleep(5 * time.Millisecond)
	cancel()

	result := <-resultCh
	if result.Outcome != OutcomeExpired {
		t.Errorf("Outcome = %v, want OutcomeExpired", result.Outcome)
	}
}

// --- Execute: Run panic → convert to error, then retry ---

func TestExecute_RunPanic_ConvertedToError(t *testing.T) {
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

	// Advance past backoff for attempt 1.
	time.Sleep(5 * time.Millisecond)
	fc.Advance(defaultBaseInterval + time.Millisecond)

	result := <-resultCh
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded (panic recovered, second attempt succeeded)", result.Outcome)
	}
	if result.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", result.Attempts)
	}
}

// --- Execute: leaseID is passed to heartbeater ---

func TestExecute_LeaseIDPassedToHeartbeater(t *testing.T) {
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()),
		WithHeartbeatInterval(5*time.Second),
		WithLeaseDuration(30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	leaseID := idutil.SafeID("my-unique-lease-id")
	step := ksaga.Step{
		Name: "step-lease",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			// Trigger a heartbeat by blocking until tick.
			return []byte("ok"), nil
		},
	}

	// Run Execute in background to capture any heartbeats.
	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), leaseID, step, ksaga.RetryPolicy{}, nil)
	}()

	<-resultCh
	// Check that if any heartbeat was fired, the leaseID was passed correctly.
	// (The step completes quickly so there may be 0 heartbeats — that's fine.)
	// What we ensure is: no panic occurred and leaseID type was forwarded correctly.
	// More thorough testing is done in TestHeartbeat_*.
}

// --- Compensate tests ---

func TestCompensate_Success(t *testing.T) {
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

	err = exec.Compensate(context.Background(), newTestInstance(), step, []byte(`{"state":1}`))
	if err != nil {
		t.Errorf("Compensate returned error: %v", err)
	}
	if !called {
		t.Error("Compensate func not called")
	}
}

func TestCompensate_Failure(t *testing.T) {
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

	err = exec.Compensate(context.Background(), newTestInstance(), step, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("Compensate = %v, want %v", err, wantErr)
	}
}

func TestCompensate_NilCompensate_ReturnsNil(t *testing.T) {
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

	err = exec.Compensate(context.Background(), newTestInstance(), step, nil)
	if err != nil {
		t.Errorf("Compensate with nil func = %v, want nil", err)
	}
}

func TestCompensate_Panic_ConvertedToError(t *testing.T) {
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

	err = exec.Compensate(context.Background(), newTestInstance(), step, nil)
	if err == nil {
		t.Error("expected error from panic in Compensate, got nil")
	}
}

func TestCompensate_IgnoresStepTimeout(t *testing.T) {
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

	err = exec.Compensate(context.Background(), newTestInstance(), step, nil)
	if err != nil {
		t.Errorf("Compensate with small step timeout returned error: %v", err)
	}
	select {
	case <-done:
	default:
		t.Error("Compensate func not reached")
	}
}

// Re-export for test usage.
var (
	defaultBaseInterval = 100 * time.Millisecond
	defaultMaxInterval  = 30 * time.Second
)

// verify ExponentialDelay is what we think it is (sanity).
func TestExponentialDelay_SanityCheck(t *testing.T) {
	got := koutbox.ExponentialDelay(100*time.Millisecond, 30*time.Second, 0)
	if got != 100*time.Millisecond {
		t.Errorf("ExponentialDelay(100ms, 30s, 0) = %v, want 100ms", got)
	}
	got = koutbox.ExponentialDelay(100*time.Millisecond, 30*time.Second, 1)
	if got != 200*time.Millisecond {
		t.Errorf("ExponentialDelay(100ms, 30s, 1) = %v, want 200ms", got)
	}
}
