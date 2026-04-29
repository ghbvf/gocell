package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLifecycle_EmptyStartStop_NoError — zero hooks, Start+Stop return nil.
func TestLifecycle_EmptyStartStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
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

	lc := NewLifecycle(LifecycleConfig{})
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

	lc := NewLifecycle(LifecycleConfig{})
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

	lc := NewLifecycle(LifecycleConfig{})
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

	lc := NewLifecycle(LifecycleConfig{})
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

// TestLifecycle_PerHookStartTimeout — hook blocks 100ms, StartTimeout=50ms →
// Start returns error containing context.DeadlineExceeded; rollback runs for
// the hooks that succeeded (none in this test, only hook failed).
func TestLifecycle_PerHookStartTimeout(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})

	var stopCalled atomic.Bool
	_ = lc.Append(Hook{
		Name: "blocker",
		OnStart: func(ctx context.Context) error {
			// Block longer than the per-hook timeout.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return nil
			}
		},
		OnStop: func(_ context.Context) error {
			stopCalled.Store(true)
			return nil
		},
		StartTimeout: 50 * time.Millisecond,
	})

	ctx := context.Background()
	err := lc.Start(ctx)
	require.Error(t, err, "Start should return error on timeout")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Hook never succeeded → its OnStop must NOT be called by rollback.
	assert.False(t, stopCalled.Load(), "OnStop of failed hook must not be called during rollback")
}

// TestLifecycle_PerHookStopTimeoutIndependent — OnStop blocks 200ms with
// StopTimeout=50ms; Stop should return within ~100ms and include DeadlineExceeded.
func TestLifecycle_PerHookStopTimeoutIndependent(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})

	_ = lc.Append(Hook{
		Name:    "slow-stopper",
		OnStart: func(_ context.Context) error { return nil },
		OnStop: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
				return nil
			}
		},
		StartTimeout: 1 * time.Second,
		StopTimeout:  50 * time.Millisecond,
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))

	start := time.Now()
	err := lc.Stop(ctx)
	elapsed := time.Since(start)

	require.Error(t, err, "Stop should return error on hook stop timeout")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// Should complete well before the full 200ms block; allow generous 150ms.
	assert.Less(t, elapsed, 150*time.Millisecond, "Stop took too long: %v (expected < 150ms)", elapsed)
}

// TestLifecycle_AppendAfterStart_ReturnsError — Append after Start returns
// ErrLifecycleAlreadyStarted.
func TestLifecycle_AppendAfterStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	err := lc.Append(Hook{Name: "late"})
	require.ErrorIs(t, err, ErrLifecycleAlreadyStarted)
	_ = lc.Stop(ctx)
}

// TestLifecycle_DoubleStart_ReturnsError — second Start returns
// ErrLifecycleAlreadyStarted; second Stop is idempotent no-op returning nil.
func TestLifecycle_DoubleStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
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
	lc := NewLifecycle(LifecycleConfig{})

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
	lc := NewLifecycle(LifecycleConfig{})

	var startCtxHadDeadline bool
	_ = lc.Append(Hook{
		Name: "no-deadline",
		OnStart: func(ctx context.Context) error {
			_, startCtxHadDeadline = ctx.Deadline()
			return nil
		},
		OnStop:       func(_ context.Context) error { return nil },
		StartTimeout: -1, // negative = no timeout
		StopTimeout:  -1,
	})

	ctx := context.Background()
	require.NoError(t, lc.Start(ctx))
	assert.False(t, startCtxHadDeadline, "expected no deadline in hook ctx when StartTimeout < 0")
	require.NoError(t, lc.Stop(ctx))
}

// TestLifecycle_NilOnStartOnStop_NoError — Hook with nil OnStart and nil OnStop
// is a valid no-op; Start and Stop return nil.
func TestLifecycle_NilOnStartOnStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
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

	lc := NewLifecycle(LifecycleConfig{Logger: logger})
	require.NoError(t, lc.Append(Hook{
		CellID:  "accesscore",
		Name:    "accesscore.initial-admin-bootstrap",
		OnStart: func(_ context.Context) error { return errors.New("boom") },
	}))

	require.Error(t, lc.Start(context.Background()), "Start must surface OnStart error")

	// Every emitted log line (hook.start, hook.start_err) must carry cell=accesscore.
	sawStart := false
	sawErr := false
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
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

	lc := NewLifecycle(LifecycleConfig{Logger: logger})
	require.NoError(t, lc.Append(Hook{
		Name:    "external.hook", // CellID intentionally omitted
		OnStart: func(_ context.Context) error { return nil },
		OnStop:  func(_ context.Context) error { return nil },
	}))
	require.NoError(t, lc.Start(context.Background()))
	require.NoError(t, lc.Stop(context.Background()))

	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
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
		DefaultStartTimeout: 20 * time.Millisecond,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		CellID: "accesscore",
		Name:   "accesscore.initial-admin-bootstrap",
		OnStart: func(_ context.Context) error {
			time.Sleep(18 * time.Millisecond)
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
		DefaultStartTimeout: 20 * time.Millisecond,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		Name: "composition-root.hook",
		OnStart: func(_ context.Context) error {
			time.Sleep(18 * time.Millisecond)
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
		DefaultStartTimeout: 1 * time.Nanosecond,
		Logger:              logger,
	})
	require.NoError(t, lc.Append(Hook{
		Name: "no-deadline",
		OnStart: func(_ context.Context) error {
			time.Sleep(time.Millisecond)
			return nil
		},
		StartTimeout: -1,
	}))
	require.NoError(t, lc.Start(context.Background()))
	rec := findLifecycleLogRecord(t, &buf, "hook.start_slow")
	assert.Nil(t, rec, "negative StartTimeout must skip hook.start_slow, got %v", rec)
}

func findLifecycleLogRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
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
