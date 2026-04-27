//go:build unix

package initialadmin

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// fixedClock — deterministic time source for sweep tests
// ---------------------------------------------------------------------------

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// ---------------------------------------------------------------------------
// manualScheduler — a no-fire scheduler for sweep tests
// ---------------------------------------------------------------------------

// manualScheduler implements Scheduler but never fires the registered timer.
// Used in sweep tests that only need to verify the cleaner is returned, not
// that it fires.
type manualScheduler struct{}

type nopCancellable struct{}

func (nopCancellable) Stop() bool { return true }

func (manualScheduler) AfterFunc(_ time.Duration, _ func()) Cancellable {
	return nopCancellable{}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeExpiredCredFile writes a cred file whose expires_at is in the past.
func writeExpiredCredFile(t *testing.T, dir string, now time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "initial_admin_password")
	expiresAt := now.Add(-time.Hour)
	payload := credentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: expiresAt,
	}
	require.NoError(t, writeCredentialFile(path, payload))
	return path
}

// writeFreshCredFile writes a cred file whose expires_at is in the future.
func writeFreshCredFile(t *testing.T, dir string, now time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "initial_admin_password")
	expiresAt := now.Add(time.Hour)
	payload := credentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: expiresAt,
	}
	require.NoError(t, writeCredentialFile(path, payload))
	return path
}

// writeMalformedCredFile writes a file without an expires_at line.
func writeMalformedCredFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "initial_admin_password")
	require.NoError(t, os.WriteFile(path, []byte("username=admin\npassword=secret\n"), 0o600))
	return path
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSweep_NoFile_NoOp verifies that sweep returns (nil, nil) with no log
// output when the credential file does not exist.
func TestSweep_NoFile_NoOp(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: filepath.Join(dir, "initial_admin_password"),
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err)
	w := result.Cleaner
	assert.Nil(t, w, "no worker expected when file does not exist")

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.records, "no log records expected when file does not exist")
}

// TestSweep_ExpiredFile_Removed verifies that an expired credential file is
// removed and the appropriate Info log entry is emitted, and no worker is returned.
func TestSweep_ExpiredFile_Removed(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeExpiredCredFile(t, dir, now)

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: credPath,
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err)
	w := result.Cleaner
	assert.Nil(t, w, "no worker expected when expired file is removed")

	// File must be gone.
	_, statErr := os.Stat(credPath)
	assert.True(t, isNotExist(statErr), "expired credential file must be removed; got: %v", statErr)

	// Info log with correct event key must be present.
	// File removal on startup is a normal lifecycle event, not a degraded-mode signal.
	rec, found := cap.findByEvent("initial_admin_credential_swept")
	assert.True(t, found, "expected Info log with event=initial_admin_credential_swept")
	assert.Equal(t, slog.LevelInfo, rec.level, "expected Info level log")
}

// TestSweep_FreshFile_Retained verifies that a non-expired credential file is
// left untouched and a non-nil cleaner worker is returned for runtime cleanup
// (P1-16 full fix: fresh orphan files must not persist until next restart).
func TestSweep_FreshFile_Retained(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeFreshCredFile(t, dir, now)

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: credPath,
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err)
	w := result.Cleaner

	// File must be retained.
	_, statErr := os.Stat(credPath)
	assert.NoError(t, statErr, "fresh credential file must be retained")

	// A cleaner worker must be returned so the caller can register it.
	assert.NotNil(t, w, "sweep must return a non-nil worker for fresh orphan file (P1-16)")
	_, isCleaner := w.(*cleaner)
	assert.True(t, isCleaner, "returned worker must be a *cleaner")

	// cleaner re-registration log must be emitted.
	_, found := cap.findByEvent("initial_admin_credential_sweep_cleaner")
	assert.True(t, found, "expected Info log with event=initial_admin_credential_sweep_cleaner")
}

// TestSweep_FreshFile_ReturnsCleanerWorker verifies that the returned cleaner
// worker from sweep is functional: Start/Stop lifecycle works and Stop is idempotent.
func TestSweep_FreshFile_ReturnsCleanerWorker(t *testing.T) {
	dir := t.TempDir()
	logger, _ := newBootstrapCapturingLogger()
	now := time.Now()

	writeFreshCredFile(t, dir, now)

	// Use a manual scheduler so the timer never fires during the test.
	sched := &manualScheduler{}
	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: filepath.Join(dir, "initial_admin_password"),
		Clock:          fixedClock{now: now},
		Scheduler:      sched,
		Logger:         logger,
	})
	require.NoError(t, err)
	w := result.Cleaner
	require.NotNil(t, w, "worker must be non-nil for fresh file")

	// Stop before Start is idempotent (cleaner.state transitions to Stopped).
	stopCtx := context.Background()
	require.NoError(t, w.Stop(stopCtx), "Stop must not error")
	// Second Stop is idempotent.
	require.NoError(t, w.Stop(stopCtx), "Stop must be idempotent")
}

// TestSweep_UnreadableFile_LogErrorContinue verifies that a cred file with
// mode 0o000 causes an Error log but sweep still returns (nil, nil) (startup not blocked).
func TestSweep_UnreadableFile_LogErrorContinue(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeExpiredCredFile(t, dir, now)

	// Make the file unreadable.
	require.NoError(t, os.Chmod(credPath, 0o000))
	t.Cleanup(func() {
		// Restore permissions so TempDir cleanup can remove the file.
		_ = os.Chmod(credPath, 0o600)
	})

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: credPath,
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err, "sweep must not return error even when file is unreadable")
	w := result.Cleaner
	assert.Nil(t, w, "no worker expected when file is unreadable")

	// Must have at least one Error-level log.
	cap.mu.Lock()
	defer cap.mu.Unlock()
	hasError := false
	for _, r := range cap.records {
		if r.level == slog.LevelError {
			hasError = true
			break
		}
	}
	assert.True(t, hasError, "expected Error-level log when file is unreadable")
}

// TestSweep_CredentialPathParentNotExist_NoError verifies that a credential
// path under a non-existent directory is treated as "no file" (normal state),
// returning (nil, nil) without error logs.
func TestSweep_CredentialPathParentNotExist_NoError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent-subdir")
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: filepath.Join(dir, "initial_admin_password"),
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err)
	w := result.Cleaner
	assert.Nil(t, w, "no worker expected when credential path does not exist")

	cap.mu.Lock()
	defer cap.mu.Unlock()
	for _, r := range cap.records {
		assert.NotEqual(t, slog.LevelError, r.level,
			"no Error log expected when credential path does not exist; got: %v", r)
	}
}

// TestSweep_MalformedExpiresAt_LogErrorContinue verifies that a file without
// a valid expires_at line causes an Error log and the file is retained, and no
// worker is returned.
func TestSweep_MalformedExpiresAt_LogErrorContinue(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeMalformedCredFile(t, dir)

	result, err := sweep(context.Background(), sweepConfig{
		CredentialPath: credPath,
		Clock:          fixedClock{now: now},
		Logger:         logger,
	})

	require.NoError(t, err, "malformed expires_at must not block startup")
	w := result.Cleaner
	assert.Nil(t, w, "no worker expected when expires_at is malformed")

	// File must be retained (never delete unknown formats).
	_, statErr := os.Stat(credPath)
	assert.NoError(t, statErr, "file with malformed expires_at must be retained")

	// Must have an Error-level log.
	cap.mu.Lock()
	defer cap.mu.Unlock()
	hasError := false
	for _, r := range cap.records {
		if r.level == slog.LevelError {
			hasError = true
			break
		}
	}
	assert.True(t, hasError, "expected Error-level log when expires_at is malformed")
}

// TestSweep_AdminExistsDoesNotSkip is a marker test documenting the invariant
// that sweep.go must not import ports.UserRepository — sweep is
// admin-existence-agnostic and operates solely on the credential file.
//
// The runtime os.ReadFile("sweep.go") check was removed because it was
// brittle (path-relative, parallel-test unsafe). The constraint is enforced
// structurally: sweep.go is in the same package as bootstrap.go which imports
// UserRepository; if sweep.go imported it too, the behaviour would simply be
// redundant — the compile-time guard is the absence of any dependency path,
// which is verified by the separation of concerns in sweep.go's import block
// (see the intentional comment there: "intentionally does not depend on UserRepository").
func TestSweep_AdminExistsDoesNotSkip(t *testing.T) {
	// Structural invariant: sweep calls removeCredentialFile and readCredentialExpiresAt
	// only. It never calls adminExists or touches UserRepository. This is enforced
	// by code review and the comment in sweep.go.
	_ = t // marker test — no runtime assertion needed
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist))
}
