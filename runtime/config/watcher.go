// Package config — watcher.go
//
// ref: spf13/viper viper.go — WatchConfig watches the parent directory for
// atomic saves and renames; fsnotify docs recommend directory-level watch.
// Adopted: watch filepath.Dir(path), filter by filepath.Base(path).
// Deviated from go-micro (file-level watch with Rename re-add): directory-level
// handles atomic replace (rename+create, remove+recreate).
//
// ref: thanos-io/thanos pkg/reloader/reloader.go — debounce with configurable
// delay interval and timer reset on each event. Adopted: WithDebounce option.
//
// ref: spf13/viper viper.go — filepath.EvalSymlinks for Kubernetes ConfigMap
// ..data symlink pivot detection. Adopted: checkSymlinkPivot on each event.
//
// ref: kubernetes/kubernetes — generation/observedGeneration pattern for tracking
// desired vs applied config state. Adopted: ObservedGenerationer on config.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchEvent carries information about a file change detected by the Watcher.
type WatchEvent struct {
	Path         string // Path of the changed file (original, not resolved).
	SymlinkPivot bool   // True if triggered by a symlink target change.
}

// WatcherOption configures a Watcher. Pass to NewWatcher.
type WatcherOption func(*watcherConfig)

// watcherConfig holds optional Watcher settings with sensible defaults.
type watcherConfig struct {
	debounce     time.Duration
	maxDebounce  time.Duration
	keyFilters   []string
	metrics      WatcherCollector
	drainTimeout time.Duration
}

func defaultWatcherConfig() watcherConfig {
	return watcherConfig{
		debounce:     100 * time.Millisecond,
		maxDebounce:  500 * time.Millisecond,
		metrics:      NoopWatcherCollector{},
		drainTimeout: 5 * time.Second,
	}
}

// WithDebounce sets the debounce interval for coalescing rapid file events.
// Events are held for this duration; if another event arrives, the timer resets.
// Set to 0 to fire callbacks immediately (no debounce).
func WithDebounce(d time.Duration) WatcherOption {
	return func(c *watcherConfig) { c.debounce = d }
}

// WithMaxDebounce sets the maximum time events can be deferred. Even if events
// keep arriving, callbacks fire after this ceiling. Prevents infinite deferral.
// Only meaningful when debounce > 0.
func WithMaxDebounce(d time.Duration) WatcherOption {
	return func(c *watcherConfig) { c.maxDebounce = d }
}

// WithKeyFilter stores key prefixes on the watcher. Bootstrap can read these
// via KeyFilters() to decide which cells to notify after a config reload.
// The watcher itself does not apply key-level filtering (it watches files,
// not config keys).
func WithKeyFilter(prefixes ...string) WatcherOption {
	return func(c *watcherConfig) {
		c.keyFilters = append(c.keyFilters[:0], prefixes...)
		sort.Strings(c.keyFilters)
	}
}

// WithMetrics injects a WatcherCollector for recording operational metrics.
func WithMetrics(m WatcherCollector) WatcherOption {
	return func(c *watcherConfig) {
		if m != nil {
			c.metrics = m
		}
	}
}

// WithDrainTimeout sets how long Close waits for in-flight callbacks to finish.
// After this timeout, Close proceeds even if callbacks are still running.
func WithDrainTimeout(d time.Duration) WatcherOption {
	return func(c *watcherConfig) { c.drainTimeout = d }
}

// Watcher monitors a file for changes by watching its parent directory.
// This correctly handles atomic replace (rename+create) and remove+recreate,
// where file-level inotify/kqueue watches would silently break due to inode
// rebinding.
//
// The watcher supports Kubernetes ConfigMap updates via ..data symlink pivot
// detection (see WithDebounce for coalescing rapid events).
type Watcher struct {
	path       string // original path (reported in WatchEvent)
	dir        string // parent directory being watched
	targetName string // base filename to filter events
	watcher    *fsnotify.Watcher
	callbacks  []func(WatchEvent)
	mu         sync.Mutex
	done       chan struct{}
	ready      chan struct{} // closed when the event loop starts
	closeOnce  sync.Once
	readyOnce  sync.Once

	cfg           watcherConfig
	lastResolved  string     // last resolved symlink target
	debounceTimer *time.Timer
	maxTimer      *time.Timer
	debounceMu    sync.Mutex     // protects timer manipulation
	callbackWg    sync.WaitGroup // tracks in-flight callback execution
	closeErr      error          // cached Close result
}

// NewWatcher creates a Watcher for the given file path. The watcher monitors
// the parent directory and filters events for the target filename.
// The watcher does not start until Start is called.
func NewWatcher(path string, opts ...WatcherOption) (*Watcher, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: abs path %s: %w", path, err)
	}

	dir := filepath.Dir(absPath)
	targetName := filepath.Base(absPath)

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: new watcher: %w", err)
	}
	if err := fw.Add(dir); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("config: watch dir %s: %w", dir, err)
	}

	cfg := defaultWatcherConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// Resolve initial symlink target for pivot detection.
	resolved, _ := filepath.EvalSymlinks(absPath)
	if resolved == "" {
		resolved = absPath
	}

	return &Watcher{
		path:         absPath,
		dir:          dir,
		targetName:   targetName,
		watcher:      fw,
		done:         make(chan struct{}),
		ready:        make(chan struct{}),
		cfg:          cfg,
		lastResolved: resolved,
	}, nil
}

// KeyFilters returns the key prefixes registered via WithKeyFilter (sorted).
func (w *Watcher) KeyFilters() []string {
	return w.cfg.keyFilters
}

// OnChange registers a callback that fires when the watched file changes.
func (w *Watcher) OnChange(fn func(WatchEvent)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, fn)
}

// Start begins watching for file changes in a goroutine. It blocks until
// Close is called or an unrecoverable error occurs.
func (w *Watcher) Start() {
	go w.loop()
}

// StartWithContext begins watching using the provided context. When ctx is
// cancelled, the watcher is closed automatically. This allows the watcher
// to be tied to a parent shutdown context.
func (w *Watcher) StartWithContext(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	go w.loop()
}

// Ready returns a channel that is closed when the event loop has started and is
// ready to process file-system events. Useful in tests to avoid time.Sleep.
func (w *Watcher) Ready() <-chan struct{} {
	return w.ready
}

// Health reports watcher readiness for bootstrap /readyz integration.
// A watcher is healthy only after its event loop has started and before it has
// been closed.
func (w *Watcher) Health() error {
	select {
	case <-w.done:
		return fmt.Errorf("config: watcher closed")
	default:
	}

	select {
	case <-w.ready:
		return nil
	default:
		return fmt.Errorf("config: watcher not started")
	}
}

func (w *Watcher) loop() {
	w.readyOnce.Do(func() { close(w.ready) })
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			symPivot, relevant := w.isRelevantEvent(event)
			if !relevant {
				continue
			}
			w.cfg.metrics.RecordEvent(w.eventType(event, symPivot))
			w.cfg.metrics.RecordLastEventTimestamp(time.Now())
			w.scheduleCallback(symPivot)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("config watcher error", slog.Any("error", err))
		case <-w.done:
			return
		}
	}
}

// isRelevantEvent checks whether an fsnotify event should trigger a callback.
// Returns (symlinkPivot, relevant).
func (w *Watcher) isRelevantEvent(event fsnotify.Event) (bool, bool) {
	// Symlink pivot detection: any Create/Remove/Rename in the watched
	// directory could indicate a symlink target change (e.g. K8s ConfigMap
	// ..data pivot). Check this first — a Create on the target filename can
	// be both a direct write AND a symlink pivot (remove+symlink recreate).
	if event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		if w.checkSymlinkPivot() {
			return true, true
		}
	}

	// Direct match: Write or Create on the target file.
	baseName := filepath.Base(event.Name)
	if baseName == w.targetName {
		if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
			return false, true
		}
	}

	return false, false
}

// checkSymlinkPivot resolves the watched path and compares against the last
// known resolved target. Returns true if the target changed (symlink pivot).
func (w *Watcher) checkSymlinkPivot() bool {
	resolved, err := filepath.EvalSymlinks(w.path)
	if err != nil {
		// File temporarily absent during pivot — not a pivot event yet.
		return false
	}

	w.mu.Lock()
	prev := w.lastResolved
	changed := resolved != prev
	if changed {
		w.lastResolved = resolved
	}
	w.mu.Unlock()
	return changed
}

func (w *Watcher) eventType(event fsnotify.Event, symPivot bool) string {
	if symPivot {
		return "symlink_pivot"
	}
	if event.Has(fsnotify.Create) {
		return "create"
	}
	return "write"
}

// scheduleCallback either fires callbacks immediately (debounce=0) or
// schedules them after the debounce window using timers.
func (w *Watcher) scheduleCallback(symPivot bool) {
	if w.cfg.debounce <= 0 {
		w.fireCallbacks(symPivot)
		return
	}

	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()

	// Reset debounce timer.
	if w.debounceTimer != nil {
		w.debounceTimer.Stop()
		w.cfg.metrics.RecordDebounceCoalesced()
	}
	w.debounceTimer = time.AfterFunc(w.cfg.debounce, func() {
		w.debounceMu.Lock()
		w.debounceTimer = nil
		if w.maxTimer != nil {
			w.maxTimer.Stop()
			w.maxTimer = nil
		}
		w.debounceMu.Unlock()
		w.fireCallbacks(symPivot)
	})

	// Start max-debounce ceiling timer if not already running.
	if w.maxTimer == nil && w.cfg.maxDebounce > 0 {
		w.maxTimer = time.AfterFunc(w.cfg.maxDebounce, func() {
			w.debounceMu.Lock()
			if w.debounceTimer != nil {
				w.debounceTimer.Stop()
				w.debounceTimer = nil
			}
			w.maxTimer = nil
			w.debounceMu.Unlock()
			w.fireCallbacks(symPivot)
		})
	}
}

func (w *Watcher) fireCallbacks(symPivot bool) {
	w.callbackWg.Add(1)
	defer w.callbackWg.Done()

	w.mu.Lock()
	cbs := make([]func(WatchEvent), len(w.callbacks))
	copy(cbs, w.callbacks)
	w.mu.Unlock()

	evt := WatchEvent{Path: w.path, SymlinkPivot: symPivot}
	for _, fn := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("config watcher callback panic", slog.Any("panic", r))
				}
			}()
			fn(evt)
		}()
	}
}

// Close stops the watcher and releases resources. It is safe to call
// concurrently from multiple goroutines. Close waits for in-flight callbacks
// up to the drain timeout before closing the underlying fsnotify watcher.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)

		// Stop debounce timers to prevent goroutine leaks.
		w.debounceMu.Lock()
		if w.debounceTimer != nil {
			w.debounceTimer.Stop()
			w.debounceTimer = nil
		}
		if w.maxTimer != nil {
			w.maxTimer.Stop()
			w.maxTimer = nil
		}
		w.debounceMu.Unlock()

		// Wait for in-flight callbacks with timeout.
		done := make(chan struct{})
		go func() {
			w.callbackWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Clean drain.
		case <-time.After(w.cfg.drainTimeout):
			slog.Warn("config: watcher drain timeout exceeded, forcing close",
				slog.Duration("timeout", w.cfg.drainTimeout))
		}

		w.closeErr = w.watcher.Close()
	})
	return w.closeErr
}
