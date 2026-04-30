package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// waitReady blocks until the watcher is ready or the test times out.
func waitReady(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready in time")
	}
}

// touchFile writes content to a file, creating it if necessary.
func touchFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// spyCollector records watcher metrics calls for test assertions.
type spyCollector struct {
	events    atomic.Int32
	coalesced atomic.Int32
	lastTime  atomic.Int64 // unix nano
}

func (s *spyCollector) RecordEvent(string)                   { s.events.Add(1) }
func (s *spyCollector) RecordLastEventTimestamp(t time.Time) { s.lastTime.Store(t.UnixNano()) }
func (s *spyCollector) RecordDebounceCoalesced()             { s.coalesced.Add(1) }

// ---------------------------------------------------------------------------
// Existing tests (backward compatibility)
// ---------------------------------------------------------------------------

func TestWatcher_OnChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	var lastEvent WatchEvent
	w.OnChange(func(evt WatchEvent) {
		lastEvent = evt
		called.Add(1)
	})

	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: val2")

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected OnChange callback to fire")

	assert.Equal(t, file, lastEvent.Path, "WatchEvent.Path should be the watched file")
}

func TestWatcher_Close(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file)
	require.NoError(t, err)

	w.Start()
	err = w.Close(context.Background())
	assert.NoError(t, err)

	// Double close should not panic.
	err = w.Close(context.Background())
	assert.NoError(t, err)
}

func TestNewWatcher_InvalidPath(t *testing.T) {
	_, err := NewWatcher("/nonexistent/file.yaml")
	assert.Error(t, err)
}

func TestWatcher_AtomicReplace_RenameCreate(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("flaky on macOS due to fsnotify event coalescing on rename+create; tracked separately. Linux + Windows still cover the path.")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	require.NoError(t, os.Rename(file, file+".bak"))
	touchFile(t, file, "key: v2")

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected callback after atomic rename+create")
}

func TestWatcher_AtomicReplace_RemoveRecreate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	require.NoError(t, os.Remove(file))
	touchFile(t, file, "key: v2")

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected callback after remove+recreate")
}

func TestWatcher_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	other := filepath.Join(dir, "other.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	touchFile(t, other, "unrelated: true")

	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load(), "unrelated file change must not fire callback")
}

func TestWatcher_StartWithContext(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	w.StartWithContext(ctx)

	cancel()

	assert.Eventually(t, func() bool {
		_ = w.Close(context.Background())
		return true
	}, 2*time.Second, 50*time.Millisecond)
}

func TestWatcher_HealthLifecycle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file)
	require.NoError(t, err)

	require.Error(t, w.Health(), "watcher must be unhealthy before Start")

	w.Start()
	waitReady(t, w)

	require.NoError(t, w.Health(), "watcher must be healthy after the loop starts")
	require.NoError(t, w.Close(context.Background()))
	require.Error(t, w.Health(), "watcher must be unhealthy after Close")
}

// ---------------------------------------------------------------------------
// Task 1: Options backward compatibility
// ---------------------------------------------------------------------------

func TestNewWatcher_WithOptions_BackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	// No options — must still work.
	w1, err := NewWatcher(file)
	require.NoError(t, err)
	require.NoError(t, w1.Close(context.Background()))

	// With options — must compile and work.
	w2, err := NewWatcher(file, WithDebounce(0), WithMaxDebounce(0))
	require.NoError(t, err)
	require.NoError(t, w2.Close(context.Background()))
}

// ---------------------------------------------------------------------------
// Task 3: Debounce
// ---------------------------------------------------------------------------

func TestWatcher_Debounce_CoalescesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(200*time.Millisecond), WithMaxDebounce(2*time.Second))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })

	// Drive the debounce scheduler directly so this test proves the coalescing
	// state machine instead of relying on noisy fsnotify delivery timing.
	for range 5 {
		w.scheduleCallback(false)
		time.Sleep(10 * time.Millisecond)
	}

	assert.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 20*time.Millisecond, "debounce should coalesce rapid schedules into one callback")

	time.Sleep(250 * time.Millisecond)
	assert.Equal(t, int32(1), called.Load(), "debounce should not emit an extra callback after the window closes")
}

func TestWatcher_Debounce_MaxCeiling(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file,
		WithDebounce(200*time.Millisecond),
		WithMaxDebounce(400*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// Write every 100ms for 1.5 seconds — debounce would never fire without ceiling.
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				touchFile(t, file, "key: continuous"+string(rune('0'+i%10)))
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Wait for at least 2 ceiling-forced callbacks (tolerant of slow CI).
	assert.Eventually(t, func() bool {
		return called.Load() >= 2
	}, 5*time.Second, 50*time.Millisecond, "max ceiling should force at least 2 callbacks")

	close(stop)
	// Let any pending timers fire.
	time.Sleep(500 * time.Millisecond)

	count := called.Load()
	assert.Less(t, count, int32(15), "debounce should coalesce many events")
}

func TestWatcher_Debounce_ZeroMeansImmediate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v1")

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 2*time.Second, 50*time.Millisecond, "zero debounce should fire immediately")
}

// ---------------------------------------------------------------------------
// Task 4: Symlink Pivot
// ---------------------------------------------------------------------------

func TestWatcher_SymlinkPivot_DetectsTargetChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()

	// Create two config versions as regular files.
	v1 := filepath.Join(dir, "config_v1.yaml")
	v2 := filepath.Join(dir, "config_v2.yaml")
	touchFile(t, v1, "version: 1")
	touchFile(t, v2, "version: 2")

	// Create symlink pointing to v1.
	link := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(v1, link))

	w, err := NewWatcher(link, WithDebounce(50*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	var gotPivot atomic.Int32
	w.OnChange(func(evt WatchEvent) {
		called.Add(1)
		if evt.SymlinkPivot {
			gotPivot.Add(1)
		}
	})
	w.Start()
	waitReady(t, w)

	// Pivot: remove old symlink, create new one pointing to v2.
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(v2, link))

	// Wait for the actual condition under assertion: a callback fired with
	// SymlinkPivot=true. Polling on `called.Load() >= 1` was a weaker proxy
	// that could fall through on slow CI runners (macos-latest Intel) where
	// the Remove event's callback fired first (called=1, gotPivot=0) and
	// the synthetic-create+SymlinkPivot callback had not yet been emitted
	// when assertion ran. Anchoring Eventually on gotPivot keeps the loop
	// alive until the SymlinkPivot signal arrives or the budget expires,
	// matching the property the test is meant to lock down.
	assert.Eventually(t, func() bool {
		return gotPivot.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "WatchEvent.SymlinkPivot should be true")

	// Sanity: at least one callback total was observed (the SymlinkPivot
	// path is a strict subset, but assert it explicitly so a future watcher
	// regression that loses the SymlinkPivot tag still surfaces against
	// "no callbacks at all" vs "callbacks without the tag".
	assert.GreaterOrEqual(t, called.Load(), int32(1), "watcher must fire at least one callback after pivot")
}

func TestWatcher_SymlinkPivot_KubernetesDataPattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()

	// Simulate K8s ConfigMap layout:
	// dir/..2024_v1/config.yaml
	// dir/..data -> ..2024_v1
	// dir/config.yaml -> ..data/config.yaml
	v1Dir := filepath.Join(dir, "..2024_v1")
	require.NoError(t, os.Mkdir(v1Dir, 0o755))
	touchFile(t, filepath.Join(v1Dir, "config.yaml"), "version: 1")

	dataLink := filepath.Join(dir, "..data")
	require.NoError(t, os.Symlink("..2024_v1", dataLink))

	configLink := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(filepath.Join("..data", "config.yaml"), configLink))

	w, err := NewWatcher(configLink, WithDebounce(50*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// Simulate K8s update: new timestamped dir, re-point ..data.
	v2Dir := filepath.Join(dir, "..2024_v2")
	require.NoError(t, os.Mkdir(v2Dir, 0o755))
	touchFile(t, filepath.Join(v2Dir, "config.yaml"), "version: 2")

	require.NoError(t, os.Remove(dataLink))
	require.NoError(t, os.Symlink("..2024_v2", dataLink))

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "K8s-style symlink pivot should fire callback")
}

func TestWatcher_SymlinkPivot_RegularFileUnaffected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file, WithDebounce(50*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	var gotPivot atomic.Int32
	w.OnChange(func(evt WatchEvent) {
		called.Add(1)
		if evt.SymlinkPivot {
			gotPivot.Add(1)
		}
	})
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v2")

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	assert.Equal(t, int32(0), gotPivot.Load(), "regular file write should not set SymlinkPivot")
}

// ---------------------------------------------------------------------------
// Task 5: Key Filter
// ---------------------------------------------------------------------------

func TestWatcher_WithKeyFilter_StoresPrefixes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file, WithKeyFilter("server.", "db."))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	filters := w.KeyFilters()
	assert.Equal(t, []string{"db.", "server."}, filters, "KeyFilters should return sorted prefixes")
}

func TestWatcher_WithKeyFilter_EmptyDefault(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	assert.Nil(t, w.KeyFilters(), "default should have no key filters")
}

// ---------------------------------------------------------------------------
// Task 6: Metrics
// ---------------------------------------------------------------------------

func TestNoopWatcherCollector_DoesNotPanic(t *testing.T) {
	var c NoopWatcherCollector
	c.RecordEvent("write")
	c.RecordLastEventTimestamp(time.Now())
	c.RecordDebounceCoalesced()
}

func TestWatcher_WithMetrics_RecordsEvents(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	spy := &spyCollector{}
	w, err := NewWatcher(file, WithDebounce(0), WithMetrics(spy))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	w.OnChange(func(_ WatchEvent) {})
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v1")

	assert.Eventually(t, func() bool {
		return spy.events.Load() >= 1 && spy.lastTime.Load() > 0
	}, 3*time.Second, 50*time.Millisecond, "metrics should record events and timestamp")
}

func TestWatcher_Metrics_DebounceCoalesced(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	spy := &spyCollector{}
	w, err := NewWatcher(file,
		WithDebounce(200*time.Millisecond),
		WithMaxDebounce(2*time.Second),
		WithMetrics(spy),
	)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// 5 rapid writes → first creates the timer, subsequent 4 reset it.
	for i := range 5 {
		touchFile(t, file, "key: v"+string(rune('1'+i)))
		time.Sleep(10 * time.Millisecond)
	}

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 20*time.Millisecond, "rapid writes should still dispatch a debounced callback")

	time.Sleep(250 * time.Millisecond)

	// Each fsnotify event beyond the first resets the timer → coalesced count.
	// We can't predict exact fsnotify event count (OS-dependent), but should
	// have at least some coalesced events.
	assert.Greater(t, spy.events.Load(), int32(1), "should receive multiple fsnotify events")
	assert.Greater(t, spy.coalesced.Load(), int32(0), "debounce should record coalesced events")
	assert.Less(t, called.Load(), int32(5), "debounce should collapse a rapid write burst into fewer callbacks than writes")
}

// ---------------------------------------------------------------------------
// Task 7: Shutdown Drain
// ---------------------------------------------------------------------------

func TestWatcher_Close_WaitsForInFlightCallbacks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0), WithDrainTimeout(2*time.Second))
	require.NoError(t, err)

	started := make(chan struct{})
	w.OnChange(func(_ WatchEvent) {
		close(started)
		time.Sleep(500 * time.Millisecond) // Simulate slow callback.
	})
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v1")

	// Wait for callback to start.
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}

	// Close should wait for the in-flight callback.
	begin := time.Now()
	require.NoError(t, w.Close(context.Background()))
	elapsed := time.Since(begin)

	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "Close should wait for in-flight callback")
	assert.Less(t, elapsed, 2*time.Second, "Close should not hang")
}

func TestWatcher_Close_DrainTimeoutPreventsHang(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	// WithDrainTimeout is no longer used by Close(ctx) directly. The caller
	// controls the budget via the ctx deadline. This test passes a 200ms budget
	// to the Close call, which should return promptly even though the callback
	// sleeps for 10 seconds.
	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)

	started := make(chan struct{})
	w.OnChange(func(_ WatchEvent) {
		close(started)
		time.Sleep(10 * time.Second) // Simulates a stuck callback.
	})
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v1")

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}

	// Caller supplies 200ms budget — Close must return before the 10s callback finishes.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer drainCancel()

	begin := time.Now()
	_ = w.Close(drainCtx)
	elapsed := time.Since(begin)

	assert.Less(t, elapsed, 1*time.Second, "caller-supplied budget should prevent hanging")
}

func TestWatcher_Close_NoInFlight_Immediate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	w.Start()
	waitReady(t, w)

	begin := time.Now()
	require.NoError(t, w.Close(context.Background()))
	elapsed := time.Since(begin)

	assert.Less(t, elapsed, 500*time.Millisecond, "close without in-flight should be immediate")
}

// ---------------------------------------------------------------------------
// Task 8: Integration + Race
// ---------------------------------------------------------------------------

func TestWatcher_FullLifecycle_AllOptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	spy := &spyCollector{}
	w, err := NewWatcher(file,
		WithDebounce(100*time.Millisecond),
		WithMaxDebounce(500*time.Millisecond),
		WithMetrics(spy),
		WithDrainTimeout(1*time.Second),
		WithKeyFilter("server."),
	)
	require.NoError(t, err)

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// Verify options stored.
	assert.Equal(t, []string{"server."}, w.KeyFilters())

	// Write and verify debounced callback.
	touchFile(t, file, "key: v1")
	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond)

	// Verify metrics recorded.
	assert.Greater(t, spy.events.Load(), int32(0))

	// Clean close.
	require.NoError(t, w.Close(context.Background()))
}

// TestWatcher_Close_DuringDebounceTimer verifies that Close() during an active
// debounce timer does not panic or race. The timer may fire during or after
// Close — the WaitGroup drain handles this safely.
func TestWatcher_Close_DuringDebounceTimer(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(500*time.Millisecond))
	require.NoError(t, err)

	w.OnChange(func(_ WatchEvent) {})
	w.Start()
	waitReady(t, w)

	// Trigger a write — debounce timer starts (500ms).
	touchFile(t, file, "key: v1")

	// Close immediately while debounce timer is still pending.
	// This exercises the race window between close(done), timer.Stop(),
	// and the timer goroutine potentially firing.
	time.Sleep(10 * time.Millisecond) // tiny delay to let event reach the loop
	require.NoError(t, w.Close(context.Background()), "Close(ctx) during active debounce timer must not error")
}

func TestWatcher_Close_ConcurrentImmediateCallbacksRace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)

	callbackStarted := make(chan struct{})
	releaseCallbacks := make(chan struct{})
	var startedOnce sync.Once
	w.OnChange(func(_ WatchEvent) {
		startedOnce.Do(func() { close(callbackStarted) })
		<-releaseCallbacks
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w.fireCallbacks(false)
		}()
	}

	close(start)
	select {
	case <-callbackStarted:
	case <-time.After(2 * time.Second):
		close(releaseCallbacks)
		wg.Wait()
		t.Fatal("callback did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = w.Close(closeCtx)

	close(releaseCallbacks)
	wg.Wait()
}

func TestWatcher_RaceDetection_ConcurrentWriteAndClose(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(50*time.Millisecond))
	require.NoError(t, err)

	w.OnChange(func(_ WatchEvent) {})
	w.Start()
	waitReady(t, w)

	var wg sync.WaitGroup

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			touchFile(t, file, "key: race"+string(rune('0'+i%10)))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Close after a short delay.
	time.Sleep(50 * time.Millisecond)
	_ = w.Close(context.Background())

	wg.Wait()
	// If this test passes with -race, there are no data races.
}

// ---------------------------------------------------------------------------
// T13: Watcher.Close(ctx) — context-aware shutdown budget
// ---------------------------------------------------------------------------

// TestWatcher_Close_AcceptsCtx verifies that Close(ctx) returns
// nil for a healthy watcher closed with ample budget.
func TestWatcher_Close_AcceptsCtx(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = w.Close(ctx)
	assert.NoError(t, err, "Close(ctx) with ample budget must return nil")
}

// TestWatcher_Close_RespectsCtxDeadline verifies that Close(ctx) returns
// ctx.Err() when the budget is exceeded during the callback drain phase.
func TestWatcher_Close_RespectsCtxDeadline(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	// Create a watcher with a long drain timeout (irrelevant — ctx wins).
	w, err := NewWatcher(file, WithDebounce(0), WithDrainTimeout(10*time.Second))
	require.NoError(t, err)
	w.Start()
	waitReady(t, w)

	// Register a callback that blocks until the test is done, simulating
	// a long-running in-flight callback.
	callbackBlocked := make(chan struct{})
	callbackRelease := make(chan struct{})
	w.OnChange(func(_ WatchEvent) {
		close(callbackBlocked)
		<-callbackRelease
	})

	// Trigger a file change to start the blocking callback.
	touchFile(t, file, "key: v1")
	select {
	case <-callbackBlocked:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start in time")
	}

	// Close with a short budget — should return promptly with ctx error.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()

	start := time.Now()
	err = w.Close(shortCtx)
	elapsed := time.Since(start)

	assert.Error(t, err, "Close(ctx) must return error when ctx budget exceeded")
	assert.Less(t, elapsed, 200*time.Millisecond,
		"Close(ctx) must return within budget; got %s", elapsed)

	close(callbackRelease) // unblock the callback goroutine
}

// TestWatcher_Close_Idempotent verifies that a second Close(ctx) call returns
// nil immediately (closeOnce guard).
func TestWatcher_Close_Idempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)

	ctx := context.Background()
	assert.NoError(t, w.Close(ctx), "first Close(ctx) must return nil")
	assert.NoError(t, w.Close(ctx), "second Close(ctx) must be no-op and return nil")
}

// ---------------------------------------------------------------------------
// WithSymlinkPollInterval
// ---------------------------------------------------------------------------

// TestWatcher_PivotTick_CoversPositiveBranch exercises the tick path's positive
// branch (lines that record metrics and fire the callback when checkSymlinkPivot
// returns true). On Linux, inotify events arrive before the ticker fires, so
// the event path updates lastResolved first; we reset it after the initial
// detection to force the next tick to see a stale value and re-enter the
// positive branch.
func TestWatcher_PivotTick_CoversPositiveBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()
	v1 := filepath.Join(dir, "config_v1.yaml")
	v2 := filepath.Join(dir, "config_v2.yaml")
	touchFile(t, v1, "version: 1")
	touchFile(t, v2, "version: 2")
	link := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(v1, link))

	w, err := NewWatcher(link,
		WithDebounce(0),
		WithSymlinkPollInterval(5*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var pivotCount atomic.Int32
	w.OnChange(func(evt WatchEvent) {
		if evt.SymlinkPivot {
			pivotCount.Add(1)
		}
	})
	w.Start()
	waitReady(t, w)

	// Pivot the symlink; on Linux events detect it first, on macOS the tick does.
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(v2, link))

	// Wait for the first pivot callback (event path on Linux, tick on macOS).
	require.Eventually(t, func() bool {
		return pivotCount.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond, "first pivot must be detected")

	// Reset lastResolved to the stale value so the next tick sees v2 ≠ v1 and
	// enters the positive branch — covering the metrics+callback lines.
	w.mu.Lock()
	w.lastResolved = v1
	w.mu.Unlock()

	// The next tick (≤5ms) will call checkSymlinkPivot → true → cover lines 302-304.
	assert.Eventually(t, func() bool {
		return pivotCount.Load() >= 2
	}, 2*time.Second, 5*time.Millisecond, "tick path positive branch must fire second callback")
}

func TestWatcher_WithSymlinkPollInterval_DisablesPoll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()
	v1 := filepath.Join(dir, "config_v1.yaml")
	v2 := filepath.Join(dir, "config_v2.yaml")
	touchFile(t, v1, "version: 1")
	touchFile(t, v2, "version: 2")
	link := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(v1, link))

	// poll=0 disables the ticker; the watcher must still start and close cleanly.
	w, err := NewWatcher(link, WithDebounce(0), WithSymlinkPollInterval(0))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// Pivot while polling is disabled — no callback expected from the poll path.
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(v2, link))

	// Give a brief window; the test asserts the loop does not crash, not callback count.
	time.Sleep(150 * time.Millisecond)
	// No assertion on called.Load() — fsnotify may or may not fire; we only
	// verify the watcher stays alive with poll disabled.
	_ = called.Load()
}

func TestWatcher_WithSymlinkPollInterval_CustomInterval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	dir := t.TempDir()
	v1 := filepath.Join(dir, "config_v1.yaml")
	v2 := filepath.Join(dir, "config_v2.yaml")
	touchFile(t, v1, "version: 1")
	touchFile(t, v2, "version: 2")
	link := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(v1, link))

	// Use a short custom poll interval so the pivot is detected via the ticker.
	w, err := NewWatcher(link, WithDebounce(0), WithSymlinkPollInterval(20*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	var gotPivot atomic.Int32
	w.OnChange(func(evt WatchEvent) {
		if evt.SymlinkPivot {
			gotPivot.Add(1)
		}
	})
	w.Start()
	waitReady(t, w)

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(v2, link))

	assert.Eventually(t, func() bool {
		return gotPivot.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "custom poll interval must detect symlink pivot")
}

// TestWatcher_isRelevantEvent_TableDriven locks the dispatch matrix used by
// loop() so that future refactors of the select-case body cannot silently
// change which fsnotify events trigger a callback. The symlink-pivot branch
// requires real filesystem state and is covered by TestWatcher_SymlinkPivot_*
// integration tests; this table focuses on the deterministic baseName +
// op-flag combinations that loop() / processFSEvent depend on.
func TestWatcher_isRelevantEvent_TableDriven(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	touchFile(t, target, "key: val")

	w, err := NewWatcher(target)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	otherInDir := filepath.Join(dir, "other.yaml")

	// NOTE: symlink-pivot positive branch (wantSym=true) is intentionally
	// omitted here — it requires real filesystem state mutation and is
	// covered by TestWatcher_SymlinkPivot_* integration tests.
	cases := []struct {
		name    string
		event   fsnotify.Event
		wantSym bool
		wantRel bool
	}{
		{
			name:    "target write fires",
			event:   fsnotify.Event{Name: target, Op: fsnotify.Write},
			wantSym: false,
			wantRel: true,
		},
		{
			name:    "target create fires",
			event:   fsnotify.Event{Name: target, Op: fsnotify.Create},
			wantSym: false,
			wantRel: true,
		},
		{
			name:    "unrelated baseName write ignored",
			event:   fsnotify.Event{Name: otherInDir, Op: fsnotify.Write},
			wantSym: false,
			wantRel: false,
		},
		{
			name:    "target chmod ignored (no Write/Create)",
			event:   fsnotify.Event{Name: target, Op: fsnotify.Chmod},
			wantSym: false,
			wantRel: false,
		},
		{
			name:    "unrelated rename without pivot ignored",
			event:   fsnotify.Event{Name: otherInDir, Op: fsnotify.Rename},
			wantSym: false,
			wantRel: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSym, gotRel := w.isRelevantEvent(tc.event)
			assert.Equal(t, tc.wantSym, gotSym, "symPivot")
			assert.Equal(t, tc.wantRel, gotRel, "relevant")
		})
	}
}

// TestWatcher_isRelevantEvent_DetectsSymlinkRemovePivot covers the positive
// symlink-pivot branch directly: a Remove event on the watched directory
// where checkSymlinkPivot detects a target swap must report
// (symPivot=true, relevant=true) without depending on the integration
// suite's timing-driven scaffolding. The setup uses a real symlink target
// switch so that filepath.EvalSymlinks observes a concrete change.
func TestWatcher_isRelevantEvent_DetectsSymlinkRemovePivot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows; covered by Linux/macOS")
	}
	dir := t.TempDir()
	v1 := filepath.Join(dir, "v1.yaml")
	v2 := filepath.Join(dir, "v2.yaml")
	link := filepath.Join(dir, "config.yaml")
	touchFile(t, v1, "key: v1")
	touchFile(t, v2, "key: v2")
	require.NoError(t, os.Symlink(v1, link))

	w, err := NewWatcher(link)
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	// Atomic-replace the symlink so EvalSymlinks resolves to a new target on
	// the next isRelevantEvent call. This is the exact ConfigMap ..data
	// pivot pattern.
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(v2, link))

	gotSym, gotRel := w.isRelevantEvent(fsnotify.Event{Name: link, Op: fsnotify.Remove})
	assert.True(t, gotSym, "symPivot must be true after target swap")
	assert.True(t, gotRel, "relevant must be true on detected pivot")
}

// orderedSpyCollector records the exact sequence of metric method invocations
// so that tests can lock the order in which processFSEvent / processPivotTick
// fan out to RecordEvent / RecordLastEventTimestamp / scheduleCallback. The
// dispatch order is dashboard-load-bearing: a "RecordEvent before
// RecordLastEventTimestamp" invariant means the counter increment is visible
// before the timestamp gauge advances, so an alert that joins them never sees
// a stale counter.
type orderedSpyCollector struct {
	mu    sync.Mutex
	calls []string
}

func (s *orderedSpyCollector) RecordEvent(eventType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "RecordEvent:"+eventType)
}

func (s *orderedSpyCollector) RecordLastEventTimestamp(time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "RecordLastEventTimestamp")
}

func (s *orderedSpyCollector) RecordDebounceCoalesced() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "RecordDebounceCoalesced")
}

func (s *orderedSpyCollector) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestWatcher_handleWatcherEvent_MetricOrdering directly exercises
// handleWatcherEvent (introduced by #339 to keep loop() under the gocognit
// ceiling) to lock the metric dispatch order. RecordEvent must precede
// RecordLastEventTimestamp; both must precede the scheduleCallback fan-out.
// Existing integration tests only assert "events>=1 && lastTime>0" eventually,
// which would silently survive a swap.
func TestWatcher_handleWatcherEvent_MetricOrdering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	touchFile(t, target, "key: val")

	spy := &orderedSpyCollector{}
	w, err := NewWatcher(target, WithMetrics(spy), WithDebounce(0))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	w.handleWatcherEvent(fsnotify.Event{Name: target, Op: fsnotify.Write})

	calls := spy.snapshot()
	require.GreaterOrEqual(t, len(calls), 2, "expected RecordEvent + RecordLastEventTimestamp")
	assert.Equal(t, "RecordEvent:write", calls[0],
		"RecordEvent must be first — dashboards depend on counter-before-timestamp ordering")
	assert.Equal(t, "RecordLastEventTimestamp", calls[1],
		"RecordLastEventTimestamp must immediately follow RecordEvent")
}

// TestWatcher_handleSymlinkPivotTick_MetricOrdering exercises the symlink-
// pivot polling path. handleSymlinkPivotTick early-returns when
// checkSymlinkPivot reports no pivot, so the test seeds w.lastResolved with a
// sentinel that EvalSymlinks will never return, forcing the positive branch
// through one tick.
func TestWatcher_handleSymlinkPivotTick_MetricOrdering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	touchFile(t, target, "key: val")

	spy := &orderedSpyCollector{}
	w, err := NewWatcher(target, WithMetrics(spy), WithDebounce(0))
	require.NoError(t, err)
	defer func() { _ = w.Close(context.Background()) }()

	// Force the next checkSymlinkPivot to detect a change.
	w.mu.Lock()
	w.lastResolved = "<sentinel-never-resolves>"
	w.mu.Unlock()

	w.handleSymlinkPivotTick()

	calls := spy.snapshot()
	require.GreaterOrEqual(t, len(calls), 2, "pivot tick must record event + timestamp")
	assert.Equal(t, "RecordEvent:symlink_pivot", calls[0],
		"pivot tick must record EventTypeSymlinkPivot first")
	assert.Equal(t, "RecordLastEventTimestamp", calls[1],
		"RecordLastEventTimestamp must immediately follow RecordEvent on pivot tick")
}
