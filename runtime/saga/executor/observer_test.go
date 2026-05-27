package executor

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// recordingObserver captures all Observer calls for assertion. Concurrent-safe.
type recordingObserver struct {
	mu        sync.Mutex
	outcomes  []recordedOutcome
	retries   []recordedRetry
	hbReasons []HeartbeatFailureReason
}

type recordedOutcome struct {
	definitionID string
	stepName     string
	outcome      Outcome
	attempts     int
}

type recordedRetry struct {
	definitionID string
	stepName     string
}

func (o *recordingObserver) ObserveOutcome(_ context.Context, defID, stepName string, outcome Outcome, attempts int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, recordedOutcome{
		definitionID: defID, stepName: stepName, outcome: outcome, attempts: attempts,
	})
}

func (o *recordingObserver) ObserveRetry(_ context.Context, defID, stepName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retries = append(o.retries, recordedRetry{definitionID: defID, stepName: stepName})
}

func (o *recordingObserver) ObserveHeartbeatFailure(_ context.Context, reason HeartbeatFailureReason) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hbReasons = append(o.hbReasons, reason)
}

func (o *recordingObserver) snapshotOutcomes() []recordedOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]recordedOutcome, len(o.outcomes))
	copy(out, o.outcomes)
	return out
}

func (o *recordingObserver) snapshotRetries() []recordedRetry {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]recordedRetry, len(o.retries))
	copy(out, o.retries)
	return out
}

func (o *recordingObserver) snapshotHBReasons() []HeartbeatFailureReason {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]HeartbeatFailureReason, len(o.hbReasons))
	copy(out, o.hbReasons)
	return out
}

// TestExecute_ObserveOutcome_Succeeded asserts ObserveOutcome fires once with
// OutcomeSucceeded after a first-attempt success.
func TestExecute_ObserveOutcome_Succeeded(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	obs := &recordingObserver{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name: "step-x",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return []byte("ok"), nil
		},
	}
	inst := newTestInstance()
	_ = exec.Execute(context.Background(), inst, "lease-1", step, ksaga.RetryPolicy{}, nil)

	got := obs.snapshotOutcomes()
	if len(got) != 1 {
		t.Fatalf("ObserveOutcome calls = %d, want 1: %+v", len(got), got)
	}
	if got[0].outcome != OutcomeSucceeded {
		t.Errorf("outcome = %v, want OutcomeSucceeded", got[0].outcome)
	}
	if got[0].definitionID != string(inst.DefinitionID) || got[0].stepName != "step-x" {
		t.Errorf("labels = (%q, %q), want (%q, step-x)", got[0].definitionID, got[0].stepName, inst.DefinitionID)
	}
	if got[0].attempts != 1 {
		t.Errorf("attempts = %d, want 1", got[0].attempts)
	}
	if len(obs.snapshotRetries()) != 0 {
		t.Error("ObserveRetry should not fire on first-attempt success")
	}
}

// TestExecute_ObserveOutcome_Failed asserts ObserveOutcome fires once with
// OutcomeFailed after the retry budget is exhausted.
func TestExecute_ObserveOutcome_Failed(t *testing.T) {
	t.Parallel()
	// Inject deterministic jitter so backoff Sleep delays are predictable.
	j := &deterministicJitter{r: rand.New(rand.NewPCG(7, 11))}         //nolint:gosec // deterministic test jitter
	jExpected := &deterministicJitter{r: rand.New(rand.NewPCG(7, 11))} //nolint:gosec // deterministic test jitter
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	obs := &recordingObserver{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()), WithObserver(obs), withJitterSource(j))
	if err != nil {
		t.Fatal(err)
	}

	const maxAttempts = 3
	step := ksaga.Step{
		Name: "step-fail",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return nil, errors.New("boom")
		},
		RetryPolicy: ksaga.RetryPolicy{MaxAttempts: maxAttempts},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-fail", step, ksaga.RetryPolicy{}, nil)
	}()

	policy := resolvedPolicy{maxAttempts: maxAttempts, base: defaultBaseInterval, max: defaultMaxInterval}
	for i := 0; i < maxAttempts-1; i++ {
		waitForOnePendingTimer(t, fc)
		fc.Advance(policy.Backoff(i, jExpected))
	}
	result := testwait.Deterministic(t, resultCh, testtime.EventuallyShort, "fail-result")

	if result.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %v, want OutcomeFailed", result.Outcome)
	}
	outcomes := obs.snapshotOutcomes()
	if len(outcomes) != 1 || outcomes[0].outcome != OutcomeFailed || outcomes[0].attempts != maxAttempts {
		t.Errorf("ObserveOutcome = %+v, want one OutcomeFailed with attempts=%d", outcomes, maxAttempts)
	}
	retries := obs.snapshotRetries()
	if len(retries) != maxAttempts-1 {
		t.Errorf("ObserveRetry count = %d, want %d", len(retries), maxAttempts-1)
	}
}

// TestExecute_ObserveOutcome_Canceled asserts a parent ctx cancellation maps
// to OutcomeCanceled and emits exactly one ObserveOutcome.
func TestExecute_ObserveOutcome_Canceled(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	obs := &recordingObserver{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stepStarted := make(chan struct{})
	step := ksaga.Step{
		Name: "step-cancel",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(stepStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(ctx, newTestInstance(), "lease-cancel", step, ksaga.RetryPolicy{}, nil)
	}()
	testwait.Deterministic(t, stepStarted, testtime.EventuallyShort, "step-started")
	cancel()
	result := testwait.Deterministic(t, resultCh, testtime.EventuallyShort, "canceled-result")

	if result.Outcome != OutcomeCanceled {
		t.Fatalf("outcome = %v, want OutcomeCanceled", result.Outcome)
	}
	outcomes := obs.snapshotOutcomes()
	if len(outcomes) != 1 || outcomes[0].outcome != OutcomeCanceled {
		t.Errorf("ObserveOutcome = %+v, want one OutcomeCanceled", outcomes)
	}
}

// staleHeartbeater returns ok=false, simulating another coordinator taking over.
type staleHeartbeater struct {
	callCount int32
}

func (s *staleHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	atomic.AddInt32(&s.callCount, 1)
	return false, nil
}

// TestExecute_ObserveHeartbeatFailure_StaleLease asserts a stale heartbeat
// fans out to ObserveHeartbeatFailure with reason=stale_lease and Outcome
// becomes LeaseLost.
func TestExecute_ObserveHeartbeatFailure_StaleLease(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	// #1181 F6 preflight needs the FIRST heartbeat to be ok=true so the step
	// actually runs; subsequent (async) ticks go stale and exercise the
	// observer fan-out. staleHeartbeater (always-false) would short-circuit
	// at preflight, never invoking the observer for an async tick.
	hb := &staleAfterFirstHeartbeater{}
	obs := &recordingObserver{}
	exec, err := NewExecutor(hb, fc,
		WithLogger(noopLogger()),
		WithObserver(obs),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	stepEntered := make(chan struct{})
	step := ksaga.Step{
		Name: "step-stale",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(stepEntered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-stale-x", step, ksaga.RetryPolicy{}, nil)
	}()
	testwait.Deterministic(t, stepEntered, testtime.EventuallyShort, "step-entered")
	// First tick fires Heartbeat → ok=false → onStale cancels runCtx → step ctx done → Outcome=LeaseLost.
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	fc.Advance(testtime.D5s)
	result := testwait.Deterministic(t, resultCh, testtime.EventuallyShort, "lease-lost-result")

	if result.Outcome != OutcomeLeaseLost {
		t.Fatalf("outcome = %v, want OutcomeLeaseLost", result.Outcome)
	}
	reasons := obs.snapshotHBReasons()
	if len(reasons) != 1 || reasons[0] != HeartbeatFailureStaleLease {
		t.Errorf("hb reasons = %v, want [stale_lease]", reasons)
	}
}

// erroringHeartbeater returns a transient err on every call (never ok=false).
type erroringHeartbeater struct {
	callCount int32
	beat      chan struct{}
}

func (e *erroringHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	atomic.AddInt32(&e.callCount, 1)
	if e.beat != nil {
		e.beat <- struct{}{}
	}
	return true, errors.New("transient infra")
}

// TestRunHeartbeat_ObserveHeartbeatFailure_InfraError asserts transient infra
// errors fan out to ObserveHeartbeatFailure with reason=infra_error and the
// goroutine continues (does not exit).
func TestRunHeartbeat_ObserveHeartbeatFailure_InfraError(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &erroringHeartbeater{beat: make(chan struct{}, 16)}
	obs := &recordingObserver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	onHBFailure := func(reason HeartbeatFailureReason) {
		obs.ObserveHeartbeatFailure(ctx, reason)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, "inst-err", "lease-err", "def-x",
			testtime.D5s, testtime.D30s, noopLogger(), noStale, onHBFailure)
	}()

	// Wait for ticker registration, then drive 3 ticks.
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	for i := 0; i < 3; i++ {
		fc.Advance(testtime.D5s)
		testwait.Deterministic(t, hb.beat, testtime.EventuallyShort, "tick")
	}
	cancel()
	wg.Wait()

	reasons := obs.snapshotHBReasons()
	if len(reasons) != 3 {
		t.Fatalf("hb reasons count = %d, want 3: %v", len(reasons), reasons)
	}
	for i, r := range reasons {
		if r != HeartbeatFailureInfraError {
			t.Errorf("hb reasons[%d] = %v, want infra_error", i, r)
		}
	}
}

// TestRunWithHeartbeat_FnReturnsNil_NoLeaseLost asserts that when fn returns
// nil before the heartbeat goroutine observes stale, the wrapper returns nil
// (heartbeat goroutine is stopped cleanly).
func TestRunWithHeartbeat_FnReturnsNil_NoLeaseLost(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	err = exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-rw1",
		func(_ context.Context) error { return nil })
	if err != nil {
		t.Errorf("RunWithHeartbeat returned %v, want nil", err)
	}
	if IsLeaseLost(err) {
		t.Error("IsLeaseLost(nil) must be false")
	}
}

// TestRunWithHeartbeat_FnError_PassedThrough asserts fn's domain error
// propagates unchanged (lease not lost).
func TestRunWithHeartbeat_FnError_PassedThrough(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithLogger(noopLogger()))
	if err != nil {
		t.Fatal(err)
	}

	domainErr := errors.New("domain-failure")
	got := exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-rw2",
		func(_ context.Context) error { return domainErr })
	if !errors.Is(got, domainErr) {
		t.Errorf("RunWithHeartbeat returned %v, want wraps %v", got, domainErr)
	}
	if IsLeaseLost(got) {
		t.Error("IsLeaseLost(domain err) must be false — domain failure overrides lease-loss")
	}
}

// TestRunWithHeartbeat_StaleLease_ReturnsLeaseLost asserts that when fn is
// long-running and the heartbeater reports ok=false, RunWithHeartbeat returns
// an error satisfying IsLeaseLost (and fn's ctx was canceled by the wrapper).
func TestRunWithHeartbeat_StaleLease_ReturnsLeaseLost(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &staleHeartbeater{}
	exec, err := NewExecutor(hb, fc,
		WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	fnEntered := make(chan struct{})
	fn := func(ctx context.Context) error {
		close(fnEntered)
		<-ctx.Done()
		return ctx.Err()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-rw3", fn)
	}()
	testwait.Deterministic(t, fnEntered, testtime.EventuallyShort, "fn-entered")
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	fc.Advance(testtime.D5s)
	got := testwait.Deterministic(t, errCh, testtime.EventuallyShort, "lease-lost-result")

	if !IsLeaseLost(got) {
		t.Errorf("got %v, want IsLeaseLost", got)
	}
}

// TestNewExecutor_NilObserver_DefaultsToNop asserts that nil Observer is silently
// ignored and the default NopObserver is kept (builder-noop semantics).
func TestNewExecutor_NilObserver_DefaultsToNop(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	exec, err := NewExecutor(hb, fc, WithObserver(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exec.observer.(NopObserver); !ok {
		t.Errorf("observer = %T, want NopObserver after WithObserver(nil)", exec.observer)
	}
}
