package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// lifecycleBlockHook is the duration the blocking hook waits (100ms), longer than its timeout.
const lifecycleBlockHook = testtime.D100ms

// lifecycleHookTimeout is the per-hook StartTimeout in PerHookStartTimeout (50ms).
const lifecycleHookTimeout = testtime.MediumPoll

// lifecycleSlowBlock is the duration a slow OnStop blocks (200ms).
const lifecycleSlowBlock = testtime.D200ms

// lifecycleStopTimeout is the StopTimeout for the slow-stopper hook (50ms).
const lifecycleStopTimeout = testtime.MediumPoll

// lifecycleStartTimeout1s is the StartTimeout for slow-stopper OnStart.
const lifecycleStartTimeout1s = testtime.D1s

// lifecycleStopElapsedMax is the generous elapsed ceiling in StopTimeoutIndependent.
const lifecycleStopElapsedMax = testtime.D150ms

// lifecycleDefaultTimeout20ms is the DefaultStartTimeout used in warn tests.
const lifecycleDefaultTimeout20ms = 20 * time.Millisecond

// lifecycleSleep18ms is the sleep duration for the near-timeout hook.
const lifecycleSleep18ms = 18 * time.Millisecond

// lifecycleDefaultTimeout1ns is the tiny DefaultStartTimeout to prevent slow-warn on -1 timeout.
const lifecycleDefaultTimeout1ns = 1 * time.Nanosecond

// lifecycleNoTimeout is the sentinel value (−1) that disables per-hook timeouts.
const lifecycleNoTimeout time.Duration = -1

// TestLifecycle_EmptyStartStop_NoError — zero hooks, Start+Stop return nil.
func TestLifecycle_EmptyStartStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_SingleHook_StartThenStop_Order — single hook A, verifies
// ["A.start", "A.stop"] order.
func TestLifecycle_SingleHook_StartThenStop_Order(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(s string) {
		mu.Lock()
		calls = append(calls, s)
		mu.Unlock()
	}

	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	_ = lc.Append(Hook{
		Name: "A",
		OnStart: func(_ context.Context) error {
			record("A.start")
			return nil
		},
		OnStop: func(_ context.Context) error {
			record("A.stop")
			return nil
		},
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	require.NoError(t, lc.Stop(ctx))

	want := []string{"A.start", "A.stop"}
	assert.Equal(t, want, calls)
}

// TestLifecycle_MultiHook_LIFOOrder — A/B/C hooks: start A→B→C, stop C→B→A.
func TestLifecycle_MultiHook_LIFOOrder(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(s string) {
		mu.Lock()
		calls = append(calls, s)
		mu.Unlock()
	}

	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	for _, name := range []string{"A", "B", "C"} {
		n := name
		_ = lc.Append(Hook{
			Name: n,
			OnStart: func(_ context.Context) error {
				record(n + ".start")
				return nil
			},
			OnStop: func(_ context.Context) error {
				record(n + ".stop")
				return nil
			},
		})
	}

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	require.NoError(t, lc.Stop(ctx))

	want := []string{"A.start", "B.start", "C.start", "C.stop", "B.stop", "A.stop"}
	assert.Equal(t, want, calls)
}

// TestLifecycle_StartFailureMidway_LIFORollback — A ok, B ok, C fails:
//   - Start returns error wrapping C's error.
//   - Subsequent Stop calls B.OnStop then A.OnStop (LIFO rollback of succeeded hooks).
//   - C.OnStop MUST NOT be called.
func TestLifecycle_StartFailureMidway_LIFORollback(t *testing.T) {
	cStopErr := errors.New("C.OnStop must not run")
	var mu sync.Mutex
	var stopCalls []string
	record := func(s string) {
		mu.Lock()
		stopCalls = append(stopCalls, s)
		mu.Unlock()
	}

	cStartErr := errors.New("C start failed")

	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	_ = lc.Append(Hook{
		Name:    "A",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(_ context.Context) error {
			record("A.stop")
			return nil
		},
	})
	_ = lc.Append(Hook{
		Name:    "B",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(_ context.Context) error {
			record("B.stop")
			return nil
		},
	})
	_ = lc.Append(Hook{
		Name:    "C",
		OnStart: func(_ context.Context) error { return cStartErr },
		OnStop: func(_ context.Context) error {
			t.Error("C.OnStop must not run")
			return cStopErr
		},
	})

	ctx := context.Background()
	startErr := lc.Start(ctx)
	require.Error(t, startErr, "Start should return error when C fails")
	require.ErrorIs(t, startErr, cStartErr)

	// After partial start failure, Stop should LIFO-rollback already-started hooks.
	require.NoError(t, lc.Stop(ctx), "Stop after partial start")

	want := []string{"B.stop", "A.stop"}
	assert.Equal(t, want, stopCalls, "rollback stop order")
}

// TestLifecycle_StopBestEffort_ErrorsCollected — middle OnStop returns error;
// the other two hooks still run; Stop returns errors.Join containing middle's error.
func TestLifecycle_StopBestEffort_ErrorsCollected(t *testing.T) {
	middleErr := errors.New("middle stop error")
	var mu sync.Mutex
	var stopCalls []string
	record := func(s string) {
		mu.Lock()
		stopCalls = append(stopCalls, s)
		mu.Unlock()
	}

	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	_ = lc.Append(Hook{
		Name:    "first",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(_ context.Context) error {
			record("first.stop")
			return nil
		},
	})
	_ = lc.Append(Hook{
		Name:    "middle",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(_ context.Context) error {
			record("middle.stop")
			return middleErr
		},
	})
	_ = lc.Append(Hook{
		Name:    "last",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(_ context.Context) error {
			record("last.stop")
			return nil
		},
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	stopErr := lc.Stop(ctx)
	require.Error(t, stopErr, "Stop should return error when middle OnStop fails")
	require.ErrorIs(t, stopErr, middleErr)

	// All three hooks still called (best-effort).
	want := []string{"last.stop", "middle.stop", "first.stop"}
	assert.Equal(t, want, stopCalls, "stop order")
}

// TestLifecycle_OnStart_NoTimeoutEnforced verifies that StartTimeout is NOT
// enforced by the lifecycle runner (supersedes ADR 202605102000 §D1).
//
// Old behavior: OnStart wrapped in context.WithTimeout(StartTimeout) — a hook
// that blocked past StartTimeout would receive DeadlineExceeded from ctx.Done().
//
// New behavior: OnStart receives the owner ctx directly. A hook that takes
// longer than StartTimeout still succeeds — the StartTimeout is informational
// (used only for the slow-start warning). Hooks must return promptly on their
// own (spawn goroutine + synchronous probe, then return).
func TestLifecycle_OnStart_NoTimeoutEnforced(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})

	startReturned := make(chan struct{})
	_ = lc.Append(Hook{
		Name: "blocker",
		OnStart: func(ctx context.Context) error {
			// Block for longer than StartTimeout (100ms > 50ms).
			// Under old semantics this would cause DeadlineExceeded.
			// Under new semantics the hook completes normally.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(lifecycleBlockHook): // 100ms
				close(startReturned)
				return nil
			}
		},
		OnStop:       func(_ context.Context) error { return nil },
		StartTimeout: lifecycleHookTimeout, // 50ms — informational only, NOT enforced
	})

	ctx := context.Background()
	err := lc.Start(ctx)
	// No error: StartTimeout is no longer a runner deadline.
	require.NoError(t, err, "Start must not return error: StartTimeout is not enforced by runner")

	// The hook ran to completion.
	select {
	case <-startReturned:
	default:
		t.Fatal("expected hook to complete and close startReturned channel")
	}

	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_PerHookStopTimeoutIndependent — OnStop blocks 200ms with
// StopTimeout=50ms; Stop should return within ~100ms and include DeadlineExceeded.
func TestLifecycle_PerHookStopTimeoutIndependent(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})

	_ = lc.Append(Hook{
		Name:    "slow-stopper",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(lifecycleSlowBlock):
				return nil
			}
		},
		StartTimeout: lifecycleStartTimeout1s,
		StopTimeout:  lifecycleStopTimeout,
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))

	start := time.Now()
	err := lc.Stop(ctx)
	elapsed := time.Since(start)

	require.Error(t, err, "Stop should return error on hook stop timeout")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// Should complete well before the full 200ms block; allow generous 150ms.
	assert.Less(t, elapsed, lifecycleStopElapsedMax, "Stop took too long: %v (expected < 150ms)", elapsed)
}

// TestLifecycle_AppendAfterStart_ReturnsError — Append after Start returns
// ErrLifecycleAlreadyStarted.
func TestLifecycle_AppendAfterStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	err := lc.Append(Hook{Name: "late"})
	require.ErrorIs(t, err, ErrLifecycleAlreadyStarted)
	_ = lc.Stop(ctx)
}

// TestLifecycle_DoubleStart_ReturnsError — second Start returns
// ErrLifecycleAlreadyStarted; second Stop is idempotent no-op returning nil.
func TestLifecycle_DoubleStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	ctx := context.Background()

	require.NoError(t, lc.Start(ctx), "first Start")

	err := lc.Start(ctx)
	require.ErrorIs(t, err, ErrLifecycleAlreadyStarted, "second Start")

	require.NoError(t, lc.Stop(ctx), "first Stop")
	// Second Stop is idempotent.
	require.NoError(t, lc.Stop(ctx), "second Stop (idempotent)")
}

// TestLifecycle_ConcurrentAppend_Safe — 100 goroutines concurrently Append
// before Start; all hooks registered without data race.
func TestLifecycle_ConcurrentAppend_Safe(t *testing.T) {
	const n = 100
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})

	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_ = lc.Append(Hook{
				Name:    "concurrent",
				OnStart: func(_ context.Context) error { return nil },
				OnStop:  func(_ context.Context) error { return nil },
			})
		}()
	}
	wg.Wait()

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx), "Start after concurrent Append")
	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_NegativeTimeout_NoDeadline — per-hook StartTimeout < 0 means no
// deadline applied; the hook completes normally even if it takes some time.
func TestLifecycle_NegativeTimeout_NoDeadline(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})

	var startCtxHadDeadline bool
	_ = lc.Append(Hook{
		Name: "no-deadline",
		OnStart: func(ctx context.Context) error {
			_, startCtxHadDeadline = ctx.Deadline()
			return nil
		},
		OnStop:       func(_ context.Context) error { return nil },
		StartTimeout: lifecycleNoTimeout, // negative = no timeout
		StopTimeout:  lifecycleNoTimeout,
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	assert.False(t, startCtxHadDeadline, "expected no deadline in hook ctx when StartTimeout < 0")
	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_NilOnStartOnStop_NoError — Hook with nil OnStart and nil OnStop
// is a valid no-op; Start and Stop return nil.
func TestLifecycle_NilOnStartOnStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{Clock: clock.Real()})
	_ = lc.Append(Hook{Name: "noop"}) // both OnStart and OnStop are nil

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_LogsCellLabel_WhenCellIDSet pins the observability contract:
// a Hook stamped with CellID (by phase3b) emits a structured "cell" slog
// attribute on every hook lifecycle event, so SRE dashboards can filter by
// owning Cell without parsing Name conventions.
//
// ref: kubernetes/kubernetes pkg/kubelet/lifecycle/handlers.go — containerName
// and pod are separate slog fields, not name-encoded.
func TestLifecycle_LogsCellLabel_WhenCellIDSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	lc := NewLifecycle(LifecycleConfig{Logger: logger, Clock: clock.Real()})
	require.NoError(t, lc.Append(Hook{
		CellID:  "accesscore",
		Name:    "accesscore.initial-admin-bootstrap",
		OnStart: func(_ context.Context) error { return errors.New("boom") },
	}))

	require.Error(t, lc.Start(context.Background()), "Start must surface OnStart error")

	// Every emitted log line (hook.start, hook.start_err) must carry cell=accesscore.
	sawStart := false
	sawErr := false
	for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec), "bad log line %q", line)
		msg, _ := rec["msg"].(string)
		cellAttr, hasCell := rec["cell"].(string)
		nameAttr, _ := rec["name"].(string)
		switch msg {
		case "hook.start":
			sawStart = true
			assert.True(t, hasCell && cellAttr == "accesscore", "hook.start missing cell label; got %v", rec)
			assert.Equal(t, "accesscore.initial-admin-bootstrap", nameAttr, "hook.start name mismatch")
		case "hook.start_err":
			sawErr = true
			assert.True(t, hasCell && cellAttr == "accesscore", "hook.start_err missing cell label; got %v", rec)
		}
	}
	assert.True(t, sawStart && sawErr, "expected both hook.start and hook.start_err in log, got buf=%s", buf.String())
}

// TestLifecycle_OmitsCellLabel_WhenCellIDEmpty pins the inverse contract:
// hooks appended via bootstrap.WithLifecycle (not phase3b) leave CellID
// empty, and logs must NOT emit cell="" noise. Empty-string observability
// labels make dashboards misleading; omission is the clean signal.
func TestLifecycle_OmitsCellLabel_WhenCellIDEmpty(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	lc := NewLifecycle(LifecycleConfig{Logger: logger, Clock: clock.Real()})
	require.NoError(t, lc.Append(Hook{
		Name:    "external.hook", // CellID intentionally omitted
		OnStart: func(_ context.Context) error { return nil },
		OnStop:  func(_ context.Context) error { return nil },
	}))
	require.NoError(t, lc.Start(context.Background()))
	require.NoError(t, lc.Stop(context.Background()))

	for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec), "bad log line %q", line)
		_, has := rec["cell"]
		assert.False(t, has, "log line must NOT carry cell label when CellID is empty: %v", rec)
	}
}

func TestLifecycle_OnStartNearTimeoutWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	lc := NewLifecycle(LifecycleConfig{
		Clock:               clock.Real(),
		DefaultStartTimeout: lifecycleDefaultTimeout20ms,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		CellID: "accesscore",
		Name:   "accesscore.initial-admin-bootstrap",
		OnStart: func(_ context.Context) error {
			time.Sleep(lifecycleSleep18ms) //archtest:allow:test-sleep slow startup hook fixture
			return nil
		},
	}))
	require.NoError(t, lc.Start(context.Background()))

	slow := findLifecycleLogRecord(t, &buf, "hook.start_slow")
	require.NotNil(t, slow, "expected hook.start_slow warning, got logs=%s", buf.String())
	assert.Equal(t, "WARN", slow["level"])
	assert.Equal(t, "accesscore", slow["cell"])
	assert.Equal(t, "accesscore.initial-admin-bootstrap", slow["name"])
	for _, key := range []string{"elapsed", "timeout", "threshold"} {
		_, ok := slow[key]
		assert.True(t, ok, "hook.start_slow missing %s: %v", key, slow)
	}
}

func TestLifecycle_OnStartNearTimeoutWarnsWithoutCellID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	lc := NewLifecycle(LifecycleConfig{
		Clock:               clock.Real(),
		DefaultStartTimeout: lifecycleDefaultTimeout20ms,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		Name: "composition-root.hook",
		OnStart: func(_ context.Context) error {
			time.Sleep(lifecycleSleep18ms) //archtest:allow:test-sleep slow startup hook fixture
			return nil
		},
	}))
	require.NoError(t, lc.Start(context.Background()))

	slow := findLifecycleLogRecord(t, &buf, "hook.start_slow")
	require.NotNil(t, slow, "expected hook.start_slow warning, got logs=%s", buf.String())
	assert.Equal(t, "composition-root.hook", slow["name"])
	_, hasCell := slow["cell"]
	assert.False(t, hasCell, "hook.start_slow must omit empty cell label: %v", slow)
}

func TestLifecycle_NegativeStartTimeoutSkipsSlowWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	lc := NewLifecycle(LifecycleConfig{
		Clock:               clock.Real(),
		DefaultStartTimeout: lifecycleDefaultTimeout1ns,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		Name: "no-deadline",
		OnStart: func(_ context.Context) error {
			time.Sleep(testtime.D1ms) //archtest:allow:test-sleep slow startup hook fixture
			return nil
		},
		StartTimeout: lifecycleNoTimeout,
	}))
	require.NoError(t, lc.Start(context.Background()))
	rec := findLifecycleLogRecord(t, &buf, "hook.start_slow")
	assert.Nil(t, rec, "negative StartTimeout must skip hook.start_slow, got %v", rec)
}

func findLifecycleLogRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		if rec["msg"] == msg {
			return rec
		}
	}
	return nil
}
