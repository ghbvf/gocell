package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func (s *spyCollector) RecordEvent(string)                { s.events.Add(1) }
func (s *spyCollector) RecordLastEventTimestamp(t time.Time) { s.lastTime.Store(t.UnixNano()) }
func (s *spyCollector) RecordDebounceCoalesced()            { s.coalesced.Add(1) }

// ---------------------------------------------------------------------------
// Existing tests (backward compatibility)
// ---------------------------------------------------------------------------

func TestWatcher_OnChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

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
	err = w.Close()
	assert.NoError(t, err)

	// Double close should not panic.
	err = w.Close()
	assert.NoError(t, err)
}

func TestNewWatcher_InvalidPath(t *testing.T) {
	_, err := NewWatcher("/nonexistent/file.yaml")
	assert.Error(t, err)
}

func TestWatcher_AtomicReplace_RenameCreate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

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
	defer func() { _ = w.Close() }()

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
	defer func() { _ = w.Close() }()

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
		_ = w.Close()
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
	require.NoError(t, w.Close())
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
	require.NoError(t, w1.Close())

	// With options — must compile and work.
	w2, err := NewWatcher(file, WithDebounce(0), WithMaxDebounce(0))
	require.NoError(t, err)
	require.NoError(t, w2.Close())
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
	defer func() { _ = w.Close() }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()
	waitReady(t, w)

	// 5 rapid writes, 10ms apart.
	for i := range 5 {
		touchFile(t, file, "key: v"+string(rune('1'+i)))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to fire.
	time.Sleep(400 * time.Millisecond)

	count := called.Load()
	assert.Equal(t, int32(1), count, "debounce should coalesce 5 rapid writes into 1 callback")
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
	defer func() { _ = w.Close() }()

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

	time.Sleep(1500 * time.Millisecond)
	close(stop)
	// Let any pending timers fire.
	time.Sleep(500 * time.Millisecond)

	count := called.Load()
	assert.GreaterOrEqual(t, count, int32(2), "max ceiling should force at least 2 callbacks in 1.5s")
	assert.Less(t, count, int32(15), "debounce should coalesce many events")
}

func TestWatcher_Debounce_ZeroMeansImmediate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

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
	defer func() { _ = w.Close() }()

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

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "symlink pivot should fire callback")

	assert.GreaterOrEqual(t, gotPivot.Load(), int32(1), "WatchEvent.SymlinkPivot should be true")
}

func TestWatcher_SymlinkPivot_KubernetesDataPattern(t *testing.T) {
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
	defer func() { _ = w.Close() }()

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
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v1")

	w, err := NewWatcher(file, WithDebounce(50*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

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
	defer func() { _ = w.Close() }()

	filters := w.KeyFilters()
	assert.Equal(t, []string{"db.", "server."}, filters, "KeyFilters should return sorted prefixes")
}

func TestWatcher_WithKeyFilter_EmptyDefault(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: val")

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

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
	defer func() { _ = w.Close() }()

	w.OnChange(func(_ WatchEvent) {})
	w.Start()
	waitReady(t, w)

	touchFile(t, file, "key: v1")

	assert.Eventually(t, func() bool {
		return spy.events.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "metrics should record events")

	assert.Greater(t, spy.lastTime.Load(), int64(0), "last event timestamp should be set")
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
	defer func() { _ = w.Close() }()

	w.OnChange(func(_ WatchEvent) {})
	w.Start()
	waitReady(t, w)

	// 5 rapid writes → first creates the timer, subsequent 4 reset it.
	for i := range 5 {
		touchFile(t, file, "key: v"+string(rune('1'+i)))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to fire.
	time.Sleep(400 * time.Millisecond)

	// Each fsnotify event beyond the first resets the timer → coalesced count.
	// We can't predict exact fsnotify event count (OS-dependent), but should
	// have at least some coalesced events.
	assert.Greater(t, spy.events.Load(), int32(1), "should receive multiple fsnotify events")
	assert.Greater(t, spy.coalesced.Load(), int32(0), "debounce should record coalesced events")
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
	require.NoError(t, w.Close())
	elapsed := time.Since(begin)

	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "Close should wait for in-flight callback")
	assert.Less(t, elapsed, 2*time.Second, "Close should not hang")
}

func TestWatcher_Close_DrainTimeoutPreventsHang(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	touchFile(t, file, "key: v0")

	w, err := NewWatcher(file, WithDebounce(0), WithDrainTimeout(200*time.Millisecond))
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

	begin := time.Now()
	_ = w.Close()
	elapsed := time.Since(begin)

	assert.Less(t, elapsed, 1*time.Second, "drain timeout should prevent hanging")
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
	require.NoError(t, w.Close())
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
	require.NoError(t, w.Close())
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
	_ = w.Close()

	wg.Wait()
	// If this test passes with -race, there are no data races.
}
