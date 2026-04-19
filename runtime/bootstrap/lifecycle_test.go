package bootstrap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLifecycle_EmptyStartStop_NoError — zero hooks, Start+Stop return nil.
func TestLifecycle_EmptyStartStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
	ctx := context.Background()
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	want := []string{"A.start", "A.stop"}
	if !equalStrSlice(calls, want) {
		t.Errorf("got %v, want %v", calls, want)
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	want := []string{"A.start", "B.start", "C.start", "C.stop", "B.stop", "A.stop"}
	if !equalStrSlice(calls, want) {
		t.Errorf("got %v, want %v", calls, want)
	}
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
	if startErr == nil {
		t.Fatal("Start should return error when C fails")
	}
	if !errors.Is(startErr, cStartErr) {
		t.Errorf("Start error should wrap cStartErr, got: %v", startErr)
	}

	// After partial start failure, Stop should LIFO-rollback already-started hooks.
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop after partial start: %v", err)
	}

	want := []string{"B.stop", "A.stop"}
	if !equalStrSlice(stopCalls, want) {
		t.Errorf("rollback stop order: got %v, want %v", stopCalls, want)
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopErr := lc.Stop(ctx)
	if stopErr == nil {
		t.Fatal("Stop should return error when middle OnStop fails")
	}
	if !errors.Is(stopErr, middleErr) {
		t.Errorf("Stop error should wrap middleErr, got: %v", stopErr)
	}

	// All three hooks still called (best-effort).
	want := []string{"last.stop", "middle.stop", "first.stop"}
	if !equalStrSlice(stopCalls, want) {
		t.Errorf("stop order: got %v, want %v", stopCalls, want)
	}
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
	if err == nil {
		t.Fatal("Start should return error on timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded in error chain, got: %v", err)
	}

	// Hook never succeeded → its OnStop must NOT be called by rollback.
	if stopCalled.Load() {
		t.Error("OnStop of failed hook must not be called during rollback")
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	err := lc.Stop(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Stop should return error on hook stop timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded in error chain, got: %v", err)
	}
	// Should complete well before the full 200ms block; allow generous 150ms.
	if elapsed > 150*time.Millisecond {
		t.Errorf("Stop took too long: %v (expected < 150ms)", elapsed)
	}
}

// TestLifecycle_AppendAfterStart_ReturnsError — Append after Start returns
// ErrLifecycleAlreadyStarted.
func TestLifecycle_AppendAfterStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
	ctx := context.Background()
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := lc.Append(Hook{Name: "late"})
	if !errors.Is(err, ErrLifecycleAlreadyStarted) {
		t.Errorf("expected ErrLifecycleAlreadyStarted, got: %v", err)
	}
	_ = lc.Stop(ctx)
}

// TestLifecycle_DoubleStart_ReturnsError — second Start returns
// ErrLifecycleAlreadyStarted; second Stop is idempotent no-op returning nil.
func TestLifecycle_DoubleStart_ReturnsError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
	ctx := context.Background()

	if err := lc.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	err := lc.Start(ctx)
	if !errors.Is(err, ErrLifecycleAlreadyStarted) {
		t.Errorf("second Start: expected ErrLifecycleAlreadyStarted, got: %v", err)
	}

	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	// Second Stop is idempotent.
	if err := lc.Stop(ctx); err != nil {
		t.Errorf("second Stop (idempotent): expected nil, got: %v", err)
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start after concurrent Append: %v", err)
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
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
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if startCtxHadDeadline {
		t.Error("expected no deadline in hook ctx when StartTimeout < 0")
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestLifecycle_NilOnStartOnStop_NoError — Hook with nil OnStart and nil OnStop
// is a valid no-op; Start and Stop return nil.
func TestLifecycle_NilOnStartOnStop_NoError(t *testing.T) {
	lc := NewLifecycle(LifecycleConfig{})
	_ = lc.Append(Hook{Name: "noop"}) // both OnStart and OnStop are nil

	ctx := context.Background()
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// equalStrSlice compares two string slices element by element.
func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
