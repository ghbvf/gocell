//go:build unix

package initialadmin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakeScheduler + fakeTimer — deterministic scheduling for tests
// ---------------------------------------------------------------------------

type fakeTimer struct {
	mu        sync.Mutex
	fn        func()
	cancelled bool
	fired     bool
}

// Stop implements Cancellable. Returns true if the timer was stopped before
// firing, false if it had already fired or was already stopped.
func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired || t.cancelled {
		return false
	}
	t.cancelled = true
	return true
}

// fire triggers the scheduled function synchronously (called by Advance).
func (t *fakeTimer) fire() {
	t.mu.Lock()
	if t.cancelled || t.fired {
		t.mu.Unlock()
		return
	}
	fn := t.fn
	t.fired = true
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

type scheduledEntry struct {
	d     time.Duration
	timer *fakeTimer
}

type fakeScheduler struct {
	mu      sync.Mutex
	entries []scheduledEntry
	elapsed time.Duration
}

// AfterFunc records the timer and returns a Cancellable. The fn is stored but
// not called until Advance moves elapsed time past d.
func (s *fakeScheduler) AfterFunc(d time.Duration, fn func()) Cancellable {
	t := &fakeTimer{fn: fn}
	s.mu.Lock()
	s.entries = append(s.entries, scheduledEntry{d: d, timer: t})
	s.mu.Unlock()
	return t
}

// Advance moves the fake clock forward by delta and fires any timers whose
// deadline has been reached.
func (s *fakeScheduler) Advance(delta time.Duration) {
	s.mu.Lock()
	s.elapsed += delta
	elapsed := s.elapsed
	// Collect timers to fire (outside of lock to avoid re-entrance issues).
	var toFire []*fakeTimer
	for _, e := range s.entries {
		if elapsed >= e.d {
			toFire = append(toFire, e.timer)
		}
	}
	s.mu.Unlock()

	for _, t := range toFire {
		t.fire()
	}
}

// ---------------------------------------------------------------------------
// capturingHandler — captures slog records for assertion
// ---------------------------------------------------------------------------

type logRecord struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

type capturingHandler struct {
	mu      sync.Mutex
	records []logRecord
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingHandler) findByEvent(event string) (logRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.attrs["event"] == event {
			return r, true
		}
	}
	return logRecord{}, false
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeTestCredFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatalf("write cred file: %v", err)
	}
}

func newTestCleaner(t *testing.T, path string, sched *fakeScheduler, handler *capturingHandler) *Cleaner {
	t.Helper()
	logger := slog.New(handler)
	c, err := NewCleaner(CleanerConfig{
		Path:      path,
		TTL:       24 * time.Hour,
		Logger:    logger,
		Scheduler: sched,
	})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}
	return c
}

// startBackground launches c.Start in a goroutine and returns a cancel func +
// a channel that closes when Start returns.
func startBackground(c *Cleaner) (cancel context.CancelFunc, done <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		_ = c.Start(ctx) //nolint:errcheck // test helper
	}()
	return cancel, ch
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCleaner_DeletesAfterTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	// Give Start a moment to register the timer.
	time.Sleep(10 * time.Millisecond)

	// Advance past 24h TTL — triggers expire().
	sched.Advance(24 * time.Hour)

	// Wait for expiry to propagate (expire runs synchronously in Advance).
	time.Sleep(20 * time.Millisecond)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected credential file to be removed after TTL, got stat err: %v", err)
	}

	cancel()
	<-done
}

func TestCleaner_StopBeforeTTL_FilePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)

	// Give Start a moment to register the timer.
	time.Sleep(10 * time.Millisecond)

	// Stop before advancing time (timer never fires).
	cancel()
	<-done

	// File must still exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected credential file to persist after early stop, got: %v", err)
	}
}

func TestCleaner_FileGoneByOperator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	// Do NOT write the file — simulates operator having already deleted it.

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	time.Sleep(10 * time.Millisecond)
	sched.Advance(24 * time.Hour)
	time.Sleep(20 * time.Millisecond)

	// expire() must not return an error (RemoveCredentialFile is idempotent
	// when the file is absent) and must log at Info level.
	rec, found := handler.findByEvent("initial_admin_credential_expired")
	if !found {
		t.Fatal("expected a log record with event=initial_admin_credential_expired")
	}
	if rec.level != slog.LevelInfo {
		t.Errorf("expected Info log for missing file, got %s", rec.level)
	}

	cancel()
	<-done
}

func TestCleaner_LogsWarnOnExpiry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	time.Sleep(10 * time.Millisecond)
	sched.Advance(24 * time.Hour)
	time.Sleep(20 * time.Millisecond)

	rec, found := handler.findByEvent("initial_admin_credential_expired")
	if !found {
		t.Fatal("expected log record with event=initial_admin_credential_expired")
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("expected Warn log on successful expiry, got %s", rec.level)
	}

	cancel()
	<-done
}

func TestCleaner_StopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)

	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done

	// Second and third Stop must not panic.
	ctx := context.Background()
	if err := c.Stop(ctx); err != nil {
		t.Errorf("second Stop returned error: %v", err)
	}
	if err := c.Stop(ctx); err != nil {
		t.Errorf("third Stop returned error: %v", err)
	}
}

func TestCleaner_StartAfterStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	// Stop before ever starting.
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("pre-start Stop returned error: %v", err)
	}

	// Start must now return an error immediately.
	err := c.Start(context.Background())
	if err == nil {
		t.Error("expected error when Start is called after Stop, got nil")
	}
}

func TestCleaner_LogsErrorOnTamperedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	// Change mode to 0644 to simulate tampering.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	sched := &fakeScheduler{}
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	time.Sleep(10 * time.Millisecond)
	sched.Advance(24 * time.Hour)
	time.Sleep(20 * time.Millisecond)

	rec, found := handler.findByEvent("initial_admin_credential_expired")
	if !found {
		t.Fatal("expected log record with event=initial_admin_credential_expired")
	}
	if rec.level != slog.LevelError {
		t.Errorf("expected Error log for tampered file, got %s", rec.level)
	}

	cancel()
	<-done
}

func TestRealScheduler_AfterFunc(t *testing.T) {
	s := RealScheduler{}
	called := make(chan struct{})
	c := s.AfterFunc(1*time.Millisecond, func() { close(called) })
	// Must return a Cancellable.
	if c == nil {
		t.Fatal("expected non-nil Cancellable")
	}
	select {
	case <-called:
		// fired as expected
	case <-time.After(500 * time.Millisecond):
		t.Error("RealScheduler timer did not fire")
	}
}

// ---------------------------------------------------------------------------
// NewCleaner validation tests
// ---------------------------------------------------------------------------

func TestNewCleaner_MissingPath(t *testing.T) {
	_, err := NewCleaner(CleanerConfig{
		Path:   "",
		TTL:    24 * time.Hour,
		Logger: slog.Default(),
	})
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestNewCleaner_ZeroTTL(t *testing.T) {
	_, err := NewCleaner(CleanerConfig{
		Path:   "/tmp/x",
		TTL:    0,
		Logger: slog.Default(),
	})
	if err == nil {
		t.Error("expected error for zero TTL")
	}
}

func TestNewCleaner_NilLogger(t *testing.T) {
	_, err := NewCleaner(CleanerConfig{
		Path:   "/tmp/x",
		TTL:    24 * time.Hour,
		Logger: nil,
	})
	if err == nil {
		t.Error("expected error for nil logger")
	}
}

func TestNewCleaner_DefaultsApplied(t *testing.T) {
	c, err := NewCleaner(CleanerConfig{
		Path:   "/tmp/x",
		TTL:    24 * time.Hour,
		Logger: slog.Default(),
		// Clock and Scheduler omitted → should default to RealClock/RealScheduler
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.clock == nil {
		t.Error("expected non-nil clock")
	}
	if c.scheduler == nil {
		t.Error("expected non-nil scheduler")
	}
}
