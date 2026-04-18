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

	"github.com/stretchr/testify/require"
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
	mu       sync.Mutex
	entries  []scheduledEntry
	elapsed  time.Duration
	timerCh  chan struct{} // closed when first timer is registered; must be non-nil before use
	timerOne sync.Once
}

// newFakeScheduler constructs a fakeScheduler with an initialised timerCh.
// Always use this constructor so timerCh is available before any goroutine
// calls AfterFunc or waitForTimer.
func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{timerCh: make(chan struct{})}
}

// waitForTimer blocks until at least one timer has been registered (i.e.
// Start has called AfterFunc). This replaces time.Sleep(10ms) pre-Advance
// waits with a deterministic signal (F-TEST-3).
func (s *fakeScheduler) waitForTimer(t *testing.T) {
	t.Helper()
	select {
	case <-s.timerCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for fakeScheduler timer registration")
	}
}

// AfterFunc records the timer and returns a Cancellable. The fn is stored but
// not called until Advance moves elapsed time past d.
func (s *fakeScheduler) AfterFunc(d time.Duration, fn func()) Cancellable {
	t := &fakeTimer{fn: fn}
	s.mu.Lock()
	s.entries = append(s.entries, scheduledEntry{d: d, timer: t})
	s.mu.Unlock()
	s.timerOne.Do(func() { close(s.timerCh) })
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
	// Write a proper credential file with expires_at so Start() can recover the TTL.
	payload := CredentialPayload{
		Username:  "admin",
		Password:  "test-pass",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := WriteCredentialFile(path, payload); err != nil {
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

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	// Wait deterministically for Start to register the timer.
	sched.waitForTimer(t)

	// Advance past 24h TTL — triggers expire() synchronously.
	sched.Advance(24 * time.Hour)

	// Await file removal (expire runs synchronously in Advance, but the OS
	// unlink may race with stat on some filesystems — require.Eventually is
	// the canonical non-sleep wait pattern).
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 500*time.Millisecond, 5*time.Millisecond,
		"expected credential file to be removed after TTL")

	cancel()
	<-done
}

func TestCleaner_StopBeforeTTL_FilePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)

	// Wait deterministically for Start to register the timer before cancelling.
	sched.waitForTimer(t)

	// Stop before advancing time (timer never fires).
	cancel()
	<-done

	// File must still exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected credential file to persist after early stop, got: %v", err)
	}
}

// TestCleaner_FileGoneByOperator verifies that when the credential file does not
// exist at Start time (operator removed it), Start logs at Info and returns
// immediately without registering a timer (P1-5: no-file no-op path).
func TestCleaner_FileGoneByOperator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	// Do NOT write the file — simulates operator having already deleted it.

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	// Start should return quickly because the file is absent.
	done := make(chan error, 1)
	go func() { done <- c.Start(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start should have returned quickly when file is absent")
	}

	// Must log at Info level.
	var rec logRecord
	require.Eventually(t, func() bool {
		r, found := handler.findByEvent("initial_admin_credential_expired")
		if found {
			rec = r
		}
		return found
	}, 500*time.Millisecond, 5*time.Millisecond,
		"expected log record with event=initial_admin_credential_expired")
	if rec.level != slog.LevelInfo {
		t.Errorf("expected Info log for missing file, got %s", rec.level)
	}
}

func TestCleaner_LogsWarnOnExpiry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	sched.waitForTimer(t)
	sched.Advance(24 * time.Hour)

	var rec logRecord
	require.Eventually(t, func() bool {
		r, found := handler.findByEvent("initial_admin_credential_expired")
		if found {
			rec = r
		}
		return found
	}, 500*time.Millisecond, 5*time.Millisecond,
		"expected log record with event=initial_admin_credential_expired")
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

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)

	sched.waitForTimer(t)
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

	sched := newFakeScheduler()
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

// TestCleaner_LogsWarnOnTamperedFile verifies that when the credential file has
// been tampered (mode changed), expire() logs at Warn level (not Error) because
// RemoveCredentialFile now deletes the file even when tampered (P1-1 fix).
func TestCleaner_LogsWarnOnTamperedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	// Change mode to 0644 to simulate tampering.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	sched.waitForTimer(t)
	sched.Advance(24 * time.Hour)

	var rec logRecord
	require.Eventually(t, func() bool {
		r, found := handler.findByEvent("initial_admin_credential_expired")
		if found {
			rec = r
		}
		return found
	}, 500*time.Millisecond, 5*time.Millisecond,
		"expected log record with event=initial_admin_credential_expired")
	if rec.level != slog.LevelWarn {
		t.Errorf("expected Warn log for tampered-but-deleted file, got %s", rec.level)
	}

	cancel()
	<-done
}

// TestCleaner_TamperedFileStillDeleted verifies that after a tampered credential
// file triggers the expiry callback, the file has been removed (P1-1: security
// intent is to destroy the credential, not to refuse because the mode changed).
func TestCleaner_TamperedFileStillDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	writeTestCredFile(t, path)

	// Simulate tampering.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c := newTestCleaner(t, path, sched, handler)

	cancel, done := startBackground(c)
	defer cancel()

	sched.waitForTimer(t)
	sched.Advance(24 * time.Hour)

	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 500*time.Millisecond, 5*time.Millisecond,
		"tampered credential file must be removed by expire(), not retained")

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
// P1-5 TTL recovery tests
// ---------------------------------------------------------------------------

// writeTestCredFileWithExpiry writes a credential file whose expires_at reflects
// the given absolute expiry time (unix timestamp). Used by P1-5 TTL recovery tests.
func writeTestCredFileWithExpiry(t *testing.T, path string, expiresAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	payload := CredentialPayload{
		Username:  "admin",
		Password:  "test-pass",
		ExpiresAt: expiresAt,
	}
	if err := WriteCredentialFile(path, payload); err != nil {
		t.Fatalf("write cred file with expiry: %v", err)
	}
}

// TestCleaner_RecoversTTLFromFileExpiresAt verifies that Start reads the
// credential file's expires_at and fires the timer after the remaining duration
// rather than the original TTL (P1-5 fix: process-restart resilience).
func TestCleaner_RecoversTTLFromFileExpiresAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")

	// Write a credential file expiring in 2h (simulates a "surviving" file
	// after a restart 22h into the original 24h TTL).
	expiresAt := time.Now().Add(2 * time.Hour)
	writeTestCredFileWithExpiry(t, path, expiresAt)

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c, err := NewCleaner(CleanerConfig{
		Path:      path,
		TTL:       24 * time.Hour, // original TTL — Start must ignore this for restart path
		Logger:    slog.New(handler),
		Scheduler: sched,
	})
	require.NoError(t, err)

	cancel, done := startBackground(c)
	defer cancel()

	// Wait for Start to register the timer (which should be ~2h, not 24h).
	sched.waitForTimer(t)

	// Advancing by 2h should fire the timer and delete the file.
	sched.Advance(2 * time.Hour)

	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 500*time.Millisecond, 5*time.Millisecond,
		"credential file must be removed after recovered TTL elapses")

	cancel()
	<-done
}

// TestCleaner_AlreadyExpired_ImmediateDelete verifies that when the expires_at
// in the credential file is in the past, Start calls expire() synchronously
// without registering a timer (P1-5 fix).
func TestCleaner_AlreadyExpired_ImmediateDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")

	// Write a credential file with an expiry in the past.
	expiresAt := time.Now().Add(-1 * time.Hour)
	writeTestCredFileWithExpiry(t, path, expiresAt)

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c, err := NewCleaner(CleanerConfig{
		Path:      path,
		TTL:       24 * time.Hour,
		Logger:    slog.New(handler),
		Scheduler: sched,
	})
	require.NoError(t, err)

	// Start should return quickly (already expired → synchronous expire → return).
	done := make(chan error, 1)
	go func() { done <- c.Start(context.Background()) }()

	select {
	case err := <-done:
		require.NoError(t, err, "Start must not error on already-expired file")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start should have returned quickly for already-expired file")
	}

	// File must be deleted.
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr),
		"file must be removed when already expired at Start time")
}

// TestCleaner_NoFile_NoOp verifies that when the credential file does not exist
// at Start time, Start logs at Info and returns without error (P1-5 fix).
func TestCleaner_NoFile_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "initial_admin_password")
	// File intentionally not created.

	sched := newFakeScheduler()
	handler := &capturingHandler{}
	c, err := NewCleaner(CleanerConfig{
		Path:      path,
		TTL:       24 * time.Hour,
		Logger:    slog.New(handler),
		Scheduler: sched,
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- c.Start(context.Background()) }()

	select {
	case err := <-done:
		require.NoError(t, err, "Start must not error when credential file is absent")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start should have returned quickly when no credential file exists")
	}

	// Expect an Info log about no cleanup needed.
	require.Eventually(t, func() bool {
		_, found := handler.findByEvent("initial_admin_credential_expired")
		return found
	}, 500*time.Millisecond, 5*time.Millisecond,
		"expected Info log when credential file not found")

	rec, found := handler.findByEvent("initial_admin_credential_expired")
	require.True(t, found)
	require.Equal(t, slog.LevelInfo, rec.level,
		"expected Info log level when credential file is absent")
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
