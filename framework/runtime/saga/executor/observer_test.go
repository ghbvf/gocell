package executor

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	ksaga "github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// recordingObserver captures all Observer calls for assertion. Concurrent-safe.
type recordingObserver struct {
	mu        sync.Mutex
	outcomes  []recordedOutcome
	retries   []recordedRetry
	hbReasons []HeartbeatFailureReason
}

type recordedOutcome struct {
	instanceID   string
	leaseID      string
	definitionID string
	stepName     string
	outcome      Outcome
	attempts     int
}

type recordedRetry struct {
	instanceID   string
	leaseID      string
	definitionID string
	stepName     string
}

func (o *recordingObserver) ObserveOutcome(
	_ context.Context,
	instanceID, leaseID idutil.SafeID,
	defID, stepName string,
	outcome Outcome,
	attempts int,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, recordedOutcome{
		instanceID: string(instanceID), leaseID: string(leaseID),
		definitionID: defID, stepName: stepName, outcome: outcome, attempts: attempts,
	})
}

func (o *recordingObserver) ObserveRetry(_ context.Context, instanceID, leaseID idutil.SafeID, defID, stepName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retries = append(o.retries, recordedRetry{
		instanceID: string(instanceID), leaseID: string(leaseID),
		definitionID: defID, stepName: stepName,
	})
}

func (o *recordingObserver) ObserveHeartbeatFailure(_ context.Context, _ idutil.SafeID, _ idutil.SafeID, reason HeartbeatFailureReason) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hbReasons = append(o.hbReasons, reason)
}

// Coordinator-emitted methods are not invoked by the Executor; no-op stubs keep
// recordingObserver satisfying the widened Observer interface.
func (o *recordingObserver) ObserveTick(_ context.Context, _ TickResult)             {}
func (o *recordingObserver) ObserveDrive(_ context.Context, _ string, _ DriveResult) {}
func (o *recordingObserver) ObserveLeaderSkip(_ context.Context, _ string, _ LeaderSkipReason) {
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

// blockingObserver simulates an out-of-contract Observer that blocks
// indefinitely. Each method signals it has been entered via the matching
// "entered" chan and then waits on release before returning. Used by
// #1210 round-3 F2 regression tests to assert the executor's bounded-wait
// helper (callObserverBounded) keeps RunWithHeartbeat / Execute making
// progress even when the observer violates its "MUST NOT block" contract.
type blockingObserver struct {
	hbEntered      chan struct{} // closed when ObserveHeartbeatFailure is called
	outcomeEntered chan struct{} // closed when ObserveOutcome is called
	release        chan struct{} // close to release the blocked observer goroutines
}

func newBlockingObserver() *blockingObserver {
	return &blockingObserver{
		hbEntered:      make(chan struct{}),
		outcomeEntered: make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (o *blockingObserver) ObserveOutcome(
	_ context.Context,
	_, _ idutil.SafeID,
	_, _ string,
	_ Outcome,
	_ int,
) {
	select {
	case <-o.outcomeEntered:
	default:
		close(o.outcomeEntered)
	}
	<-o.release
}

func (o *blockingObserver) ObserveRetry(_ context.Context, _, _ idutil.SafeID, _, _ string) {
	<-o.release
}

func (o *blockingObserver) ObserveHeartbeatFailure(_ context.Context, _, _ idutil.SafeID, _ HeartbeatFailureReason) {
	select {
	case <-o.hbEntered:
	default:
		close(o.hbEntered)
	}
	<-o.release
}

// Coordinator-emitted methods are not exercised by the Executor blocking-path
// regressions; no-op stubs satisfy the interface.
func (o *blockingObserver) ObserveTick(_ context.Context, _ TickResult)             {}
func (o *blockingObserver) ObserveDrive(_ context.Context, _ string, _ DriveResult) {}
func (o *blockingObserver) ObserveLeaderSkip(_ context.Context, _ string, _ LeaderSkipReason) {
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
	if got[0].instanceID != string(inst.ID) {
		t.Errorf("instanceID = %q, want %q", got[0].instanceID, inst.ID)
	}
	if got[0].leaseID != "lease-1" {
		t.Errorf("leaseID = %q, want lease-1", got[0].leaseID)
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
	result := testwait.Deterministic(t, resultCh, "fail-result")

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
	testwait.Deterministic(t, stepStarted, "step-started")
	cancel()
	result := testwait.Deterministic(t, resultCh, "canceled-result")

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
	exec, err := NewExecutor(
		hb, fc,
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
	testwait.Deterministic(t, stepEntered, "step-entered")
	// First tick fires Heartbeat → ok=false → onStale cancels runCtx → step ctx done → Outcome=LeaseLost.
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	fc.Advance(testtime.D5s)
	result := testwait.Deterministic(t, resultCh, "lease-lost-result")

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
		obs.ObserveHeartbeatFailure(ctx, "inst-err", "lease-err", reason)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(ctx, fc, hb, heartbeatConfig{
			InstanceID:    "inst-err",
			LeaseID:       "lease-err",
			DefinitionID:  "def-x",
			Interval:      testtime.D5s,
			LeaseDuration: testtime.D30s,
		}, noopLogger(), noStale, onHBFailure)
	}()

	// Wait for ticker registration, then drive 3 ticks.
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	for i := 0; i < 3; i++ {
		fc.Advance(testtime.D5s)
		testwait.Deterministic(t, hb.beat, "tick")
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
// long-running and the heartbeater reports ok=false on an async tick,
// RunWithHeartbeat returns an error satisfying IsLeaseLost (and fn's ctx was
// canceled by the wrapper). Uses staleAfterFirstHeartbeater so the preflight
// heartbeat passes (ok=true) and the step actually starts; the first async
// tick returns ok=false to trigger lease-lost cancellation (#1210 F3).
func TestRunWithHeartbeat_StaleLease_ReturnsLeaseLost(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &staleAfterFirstHeartbeater{} // first call (preflight) ok=true; subsequent stale
	exec, err := NewExecutor(
		hb, fc,
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
	testwait.Deterministic(t, fnEntered, "fn-entered")
	testwait.External(t, "fakeclock-ticker-registration",
		func() bool { return fc.PendingTickers() >= 1 },
		testtime.EventuallyShort, testtime.FastPoll, "no ticker")
	fc.Advance(testtime.D5s)
	got := testwait.Deterministic(t, errCh, "lease-lost-result")

	if !IsLeaseLost(got) {
		t.Errorf("got %v, want IsLeaseLost", got)
	}
}

// TestRunWithHeartbeat_PreflightStaleLease_ReturnsLeaseLost asserts that
// RunWithHeartbeat returns errLeaseLost immediately when the preflight
// heartbeat observes ok=false, without invoking fn at all (#1210 F3).
func TestRunWithHeartbeat_PreflightStaleLease_ReturnsLeaseLost(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &staleHeartbeater{} // always returns ok=false
	exec, err := NewExecutor(
		hb, fc,
		WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	fnCalled := false
	got := exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-preflight",
		func(_ context.Context) error {
			fnCalled = true
			return nil
		})

	if !IsLeaseLost(got) {
		t.Errorf("got %v, want IsLeaseLost (preflight stale)", got)
	}
	if fnCalled {
		t.Error("fn must not be called when preflight heartbeat is stale")
	}
}

// TestRunWithHeartbeat_BlockingObserver_DoesNotStall asserts the bounded
// observer-call wait (#1210 round-3 F2): an out-of-contract Observer that
// blocks indefinitely on ObserveHeartbeatFailure does NOT stall the
// RunWithHeartbeat preflight path. Without the bound, the preflight-stale
// branch would block forever in safeObserveHeartbeatFailure and
// RunWithHeartbeat would never return.
func TestRunWithHeartbeat_BlockingObserver_DoesNotStall(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &staleHeartbeater{} // preflight returns ok=false → blocks on observer
	obs := newBlockingObserver()
	defer close(obs.release) // unblock observer goroutines on test exit (no leak across tests)

	exec, err := NewExecutor(
		hb, fc,
		WithLogger(noopLogger()),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
		WithObserver(obs),
		WithObserverCallDeadline(testtime.D20ms),
	)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-blocking-obs",
			func(_ context.Context) error {
				t.Error("fn must not be called when preflight heartbeat is stale")
				return nil
			})
	}()

	// Wait for the observer call to enter so we know the bounded timer was
	// created before we advance the clock past its deadline.
	testwait.Deterministic(t, obs.hbEntered, "observer-call-entered")

	// Advance past the bounded deadline; the timer fires, callObserverBounded
	// logs Warn and returns, and the preflight branch returns errLeaseLost.
	fc.Advance(testtime.D50ms)

	got := testwait.Deterministic(t, errCh,
		"RunWithHeartbeat must return despite blocked observer")
	if !IsLeaseLost(got) {
		t.Errorf("got %v, want IsLeaseLost (preflight stale)", got)
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

// panicObserver is an out-of-contract Observer that panics in every callback.
// Used to exercise recoverObserverPanic's correlation-logging path (F1).
type panicObserver struct{}

func (panicObserver) ObserveOutcome(_ context.Context, _, _ idutil.SafeID, _, _ string, _ Outcome, _ int) {
	panic("observe outcome boom")
}

func (panicObserver) ObserveRetry(_ context.Context, _, _ idutil.SafeID, _, _ string) {
	panic("observe retry boom")
}

func (panicObserver) ObserveHeartbeatFailure(_ context.Context, _, _ idutil.SafeID, _ HeartbeatFailureReason) {
	panic("observe hb boom")
}

func (panicObserver) ObserveTick(_ context.Context, _ TickResult) { panic("observe tick boom") }

func (panicObserver) ObserveDrive(_ context.Context, _ string, _ DriveResult) {
	panic("observe drive boom")
}

func (panicObserver) ObserveLeaderSkip(_ context.Context, _ string, _ LeaderSkipReason) {
	panic("observe leader-skip boom")
}

// TestCoordinatorEmittedEnums_FrozenWireValues asserts the wire-stable string
// values of the Coordinator-emitted label enums. These values are operator-facing
// (metric labels + slog fields) and are frozen by archtest
// SAGA-METRIC-LABEL-VALUES-FROZEN-01; this unit test pins them at the source.
func TestCoordinatorEmittedEnums_FrozenWireValues(t *testing.T) {
	t.Parallel()
	cases := []struct{ got, want string }{
		{string(TickClaimed), "claimed"},
		{string(TickEmpty), "empty"},
		{string(TickError), "error"},
		{string(DriveOK), "ok"},
		{string(DriveError), "error"},
		{string(LeaderSkipContended), "contended"},
		{string(LeaderSkipCtxCanceled), "ctx_canceled"},
		{string(LeaderSkipBackendError), "backend_error"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("enum wire value = %q, want %q", c.got, c.want)
		}
	}
}

// TestNopObserver_CoordinatorEmitted_DoNotPanic asserts the NopObserver's
// Coordinator-emitted methods are safe no-ops.
func TestNopObserver_CoordinatorEmitted_DoNotPanic(t *testing.T) {
	t.Parallel()
	var o NopObserver
	o.ObserveTick(context.Background(), TickClaimed)
	o.ObserveDrive(context.Background(), "def-x", DriveOK)
	o.ObserveLeaderSkip(context.Background(), "def-x", LeaderSkipContended)
}

// TestObserverCall_Timeout_LogsCorrelation asserts the bounded-wait timeout log
// (callObserverBounded) carries instance_id and method so an operator can
// correlate an observer-deadline breach back to the instance. lease_id presence
// is now structurally enforced by the SAGA-SLOG-INSTANCE-FIELDS-CALLER-01
// funnel; this test keeps the instance_id+method correlation rationale.
func TestObserverCall_Timeout_LogsCorrelation(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	obs := newBlockingObserver()
	defer close(obs.release) // unblock the leaked observer goroutine on test exit
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	exec, err := NewExecutor(
		&alwaysOKHeartbeater{}, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
		WithObserver(obs),
		WithObserverCallDeadline(testtime.D20ms),
	)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.safeObserveHeartbeatFailure(context.Background(), "test-instance-id", "lease-timeout-log",
			HeartbeatFailureStaleLease)
	}()

	// Wait for the observer call to enter so the bounded timer exists, then
	// advance past its deadline to fire the timeout branch.
	testwait.Deterministic(t, obs.hbEntered, "observer-call-entered")
	fc.Advance(testtime.D50ms)
	// Synchronize directly on the bounded observer helper rather than the full
	// RunWithHeartbeat preflight path. The production branch under test is the
	// same callObserverBounded timeout log, but this avoids heartbeat join
	// scheduling noise that can trip slowgate under CI load.
	testwait.Deterministic(t, done, "observer timeout log emitted")

	entry := sloghelper.FindLogEntry(buf.String(), "observer call exceeded deadline")
	if entry == nil {
		t.Fatalf("expected an observer-deadline WARN log; logs=%s", buf.String())
	}
	if entry["instance_id"] != "test-instance-id" {
		t.Errorf("timeout log instance_id = %v, want test-instance-id", entry["instance_id"])
	}
	if entry["method"] != "ObserveHeartbeatFailure" {
		t.Errorf("timeout log method = %v, want ObserveHeartbeatFailure", entry["method"])
	}
}

// TestObserverCall_Panic_LogsCorrelation asserts the recoverObserverPanic log
// carries instance_id + method (F1, panic branch). lease_id presence is now
// structurally enforced by the SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 funnel.
// Uses a direct safeObserveOutcome call with a panicking observer — recovery
// is synchronous, so no clock dance is needed: callObserverBounded returns via
// <-done only after recoverObserverPanic (deferred LIFO before close(done))
// has logged.
func TestObserverCall_Panic_LogsCorrelation(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &alwaysOKHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc, WithLogger(logger), WithObserver(panicObserver{}))
	if err != nil {
		t.Fatal(err)
	}

	exec.safeObserveOutcome(context.Background(), "test-instance-id", "lease-panic-log",
		"test-def-id", "step-x", OutcomeSucceeded, 1)

	entry := sloghelper.FindLogEntry(buf.String(), "observer call panicked")
	if entry == nil {
		t.Fatalf("expected an observer-panic WARN log; logs=%s", buf.String())
	}
	if entry["instance_id"] != "test-instance-id" {
		t.Errorf("panic log instance_id = %v, want test-instance-id", entry["instance_id"])
	}
	if entry["method"] != "ObserveOutcome" {
		t.Errorf("panic log method = %v, want ObserveOutcome", entry["method"])
	}
}
