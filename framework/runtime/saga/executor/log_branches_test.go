package executor

// Tests for the 5 log branches in executor.go that were not covered by
// existing tests after the lease_id-logging refactor.
//
// Branch map (executor.go line → test):
//   387  "preflight heartbeat reported stale lease"          → TestExecuteInner_PreflightStale_LogsMessage
//   397  "preflight heartbeat infra error (fail-open)"       → TestExecuteInner_PreflightInfraError_LogsMessage
//   434  "heartbeat goroutine recovered from panic" (Execute) → TestExecuteInner_HeartbeatGoroutinePanic_LogsMessage
//   519  "preflight heartbeat infra error (fail-open, RunWithHeartbeat)" → TestRunWithHeartbeat_PreflightInfraError_LogsMessage
//   543  "heartbeat goroutine recovered from panic" (RWH)    → TestRunWithHeartbeat_HeartbeatGoroutinePanic_LogsMessage

import (
	"context"
	"log/slog"
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

// -----------------------------------------------------------------------------
// Branch 1 — executor.go:387
// "saga executor: preflight heartbeat reported stale lease"
// Path: executeInner preflight call returns (false, nil).
// The staleHeartbeater defined in observer_test.go always returns (false,nil).
// -----------------------------------------------------------------------------

// TestExecuteInner_PreflightStale_LogsMessage verifies that when the preflight
// heartbeat returns ok=false the executor logs the stale-lease Info message and
// returns OutcomeLeaseLost without running the step.
func TestExecuteInner_PreflightStale_LogsMessage(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &staleHeartbeater{} // always returns (false, nil)
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	stepCalled := false
	step := ksaga.Step{
		Name: "step-preflight-stale",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			stepCalled = true
			return nil, nil
		},
	}

	result := exec.Execute(context.Background(), newTestInstance(), "lease-preflight-stale", step, ksaga.RetryPolicy{}, nil)

	if result.Outcome != OutcomeLeaseLost {
		t.Errorf("Outcome = %v, want OutcomeLeaseLost", result.Outcome)
	}
	if stepCalled {
		t.Error("step.Run must not be called when preflight heartbeat is stale")
	}

	entry := sloghelper.FindLogEntry(buf.String(), "preflight heartbeat reported stale lease")
	if entry == nil {
		t.Fatalf("expected log line 387 'preflight heartbeat reported stale lease'; logs:\n%s", buf.String())
	}
}

// -----------------------------------------------------------------------------
// Branch 2 — executor.go:397
// "saga executor: preflight heartbeat infra error (fail-open)"
// Path: executeInner preflight call returns (_, non-nil err).
// The erroringHeartbeater defined in observer_test.go always returns
// (true, errors.New("transient infra")), so err != nil → infra branch.
// Fail-open means execution continues; use a trivial step that succeeds.
// -----------------------------------------------------------------------------

// TestExecuteInner_PreflightInfraError_LogsMessage verifies that an infra error
// on the preflight heartbeat emits the fail-open Warn log and continues execution
// (the step still runs and succeeds).
func TestExecuteInner_PreflightInfraError_LogsMessage(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	// erroringHeartbeater returns (true, non-nil-err) on every call.
	// Preflight sees err != nil → logs line 397 then fail-opens.
	// The async heartbeat goroutine's calls will also get err, but the goroutine
	// continues (same infra-error path in runHeartbeat).
	hb := &erroringHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	step := ksaga.Step{
		Name: "step-infra-error",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return []byte("ok"), nil
		},
	}

	result := exec.Execute(context.Background(), newTestInstance(), "lease-infra-err", step, ksaga.RetryPolicy{}, nil)

	// Fail-open: the step succeeded despite the preflight error.
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("Outcome = %v, want OutcomeSucceeded (fail-open after infra error)", result.Outcome)
	}

	entry := sloghelper.FindLogEntry(buf.String(), "preflight heartbeat infra error (fail-open)")
	if entry == nil {
		t.Fatalf("expected log line 397 'preflight heartbeat infra error (fail-open)'; logs:\n%s", buf.String())
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
}

// -----------------------------------------------------------------------------
// Branch 3 — executor.go:434
// "saga executor: heartbeat goroutine recovered from panic" (inside executeInner)
// Path: heartbeat goroutine's Heartbeat call panics on an async ticker tick.
// The panicAfterPreflightHeartbeater returns (true,nil) on call 1 (preflight)
// so executeInner proceeds and starts the goroutine. On call ≥2 it panics,
// the deferred recover() fires and logs line 434, then calls onStale which
// cancels runCtx, unblocking the blocking step.
// -----------------------------------------------------------------------------

// panicAfterPreflightHeartbeater returns (true, nil) on the first call and
// panics on all subsequent calls. The first call is the synchronous preflight
// check inside executeInner / RunWithHeartbeat; subsequent calls come from the
// heartbeat goroutine's ticker ticks.
type panicAfterPreflightHeartbeater struct {
	calls int32
}

func (p *panicAfterPreflightHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	n := atomic.AddInt32(&p.calls, 1)
	if n >= 2 {
		panic("hb boom")
	}
	return true, nil
}

// TestExecuteInner_HeartbeatGoroutinePanic_LogsMessage drives the heartbeat
// goroutine into a panic on the first async tick. The recover() logs line 434
// and calls onStale, which cancels the running step's context.
func TestExecuteInner_HeartbeatGoroutinePanic_LogsMessage(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &panicAfterPreflightHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	// A step that blocks until its context is canceled. The panic recover()
	// calls onStale which sets the cancel cause to errLeaseLost, unblocking the
	// step and causing Execute to return OutcomeLeaseLost.
	stepEntered := make(chan struct{})
	step := ksaga.Step{
		Name: "step-hb-panic",
		Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			close(stepEntered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- exec.Execute(context.Background(), newTestInstance(), "lease-hb-panic", step, ksaga.RetryPolicy{}, nil)
	}()

	// Wait for the step to start (guarantees the heartbeat goroutine is running).
	testwait.Deterministic(t, stepEntered, "step-entered")

	// Wait for the heartbeat ticker to be registered, then advance to fire it.
	// The goroutine will panic on the Heartbeat call and the recover() fires.
	waitForOneTicker(t, fc)
	fc.Advance(testtime.D5s)

	// Execute must return (the panic recover called onStale → runCtx canceled).
	result := testwait.Deterministic(t, resultCh, "execute-after-hb-panic")

	// The panic recover calls onStale → errLeaseLost cause → OutcomeLeaseLost.
	if result.Outcome != OutcomeLeaseLost {
		t.Errorf("Outcome = %v, want OutcomeLeaseLost after heartbeat goroutine panic", result.Outcome)
	}

	entry := sloghelper.FindLogEntry(buf.String(), "heartbeat goroutine recovered from panic")
	if entry == nil {
		t.Fatalf("expected log line 434 'heartbeat goroutine recovered from panic'; logs:\n%s", buf.String())
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
}

// -----------------------------------------------------------------------------
// Branch 4 — executor.go:519
// "saga executor: preflight heartbeat infra error (fail-open, RunWithHeartbeat)"
// Path: RunWithHeartbeat preflight call returns (_, non-nil err).
// Same erroringHeartbeater used for branch 2, but called via RunWithHeartbeat.
// Fail-open: fn still executes and returns nil.
// -----------------------------------------------------------------------------

// TestRunWithHeartbeat_PreflightInfraError_LogsMessage verifies that an infra
// error on the RunWithHeartbeat preflight emits the fail-open Warn log (line 519)
// and continues to invoke fn.
func TestRunWithHeartbeat_PreflightInfraError_LogsMessage(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &erroringHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	fnCalled := false
	got := exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-rwh-infra",
		func(_ context.Context) error {
			fnCalled = true
			return nil
		})

	if got != nil {
		t.Errorf("RunWithHeartbeat returned %v, want nil (fail-open after infra error)", got)
	}
	if !fnCalled {
		t.Error("fn must be called on infra-error fail-open path")
	}

	entry := sloghelper.FindLogEntry(buf.String(), "preflight heartbeat infra error (fail-open, RunWithHeartbeat)")
	if entry == nil {
		t.Fatalf("expected log line 519 'preflight heartbeat infra error (fail-open, RunWithHeartbeat)'; logs:\n%s", buf.String())
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
}

// -----------------------------------------------------------------------------
// Branch 5 — executor.go:543
// "saga executor: heartbeat goroutine recovered from panic" (inside RunWithHeartbeat)
// Same panic-on-tick pattern as branch 3 but on the RunWithHeartbeat path.
// The recover() calls onStale → runCtx canceled → fn ctx.Done() → RWH returns
// errLeaseLost (satisfies IsLeaseLost).
// -----------------------------------------------------------------------------

// TestRunWithHeartbeat_HeartbeatGoroutinePanic_LogsMessage drives the heartbeat
// goroutine inside RunWithHeartbeat into a panic. The recover() logs line 543
// and calls onStale, which cancels fn's context.
func TestRunWithHeartbeat_HeartbeatGoroutinePanic_LogsMessage(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	hb := &panicAfterPreflightHeartbeater{}
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec, err := NewExecutor(hb, fc,
		WithLogger(logger),
		WithHeartbeatInterval(testtime.D5s),
		WithLeaseDuration(testtime.D30s),
	)
	if err != nil {
		t.Fatal(err)
	}

	// fn blocks until its context is canceled. The panic recover in the
	// heartbeat goroutine calls onStale → errLeaseLost cause → ctx.Done().
	fnEntered := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- exec.RunWithHeartbeat(context.Background(), newTestInstance(), "lease-rwh-panic",
			func(ctx context.Context) error {
				close(fnEntered)
				<-ctx.Done()
				return ctx.Err()
			})
	}()

	// Wait for fn to start (heartbeat goroutine is now running).
	testwait.Deterministic(t, fnEntered, "fn-entered")

	// Advance the clock to fire the ticker → Heartbeat panics → recover fires.
	waitForOneTicker(t, fc)
	fc.Advance(testtime.D5s)

	got := testwait.Deterministic(t, errCh, "rwh-after-hb-panic")

	if !IsLeaseLost(got) {
		t.Errorf("got %v, want IsLeaseLost (heartbeat goroutine panicked → onStale)", got)
	}

	entry := sloghelper.FindLogEntry(buf.String(), "heartbeat goroutine recovered from panic")
	if entry == nil {
		t.Fatalf("expected log line 543 'heartbeat goroutine recovered from panic'; logs:\n%s", buf.String())
	}
	if entry["level"] != "WARN" {
		t.Errorf("log level = %v, want WARN", entry["level"])
	}
}
