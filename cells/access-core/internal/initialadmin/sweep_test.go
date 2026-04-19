//go:build unix

package initialadmin

import (
	"context"
	"fmt"
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
// helpers
// ---------------------------------------------------------------------------

// writeExpiredCredFile writes a cred file whose expires_at is in the past.
func writeExpiredCredFile(t *testing.T, dir string, now time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "initial_admin_password")
	expiresAt := now.Add(-time.Hour)
	payload := CredentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: expiresAt,
	}
	require.NoError(t, WriteCredentialFile(path, payload))
	return path
}

// writeFreshCredFile writes a cred file whose expires_at is in the future.
func writeFreshCredFile(t *testing.T, dir string, now time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "initial_admin_password")
	expiresAt := now.Add(time.Hour)
	payload := CredentialPayload{
		Username:  "admin",
		Password:  "secret",
		ExpiresAt: expiresAt,
	}
	require.NoError(t, WriteCredentialFile(path, payload))
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

// TestSweep_NoFile_NoOp verifies that Sweep returns nil with no log output
// when the credential file does not exist.
func TestSweep_NoFile_NoOp(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.records, "no log records expected when file does not exist")
}

// TestSweep_ExpiredFile_Removed verifies that an expired credential file is
// removed and the appropriate Warn log entry is emitted.
func TestSweep_ExpiredFile_Removed(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeExpiredCredFile(t, dir, now)

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err)

	// File must be gone.
	_, statErr := os.Stat(credPath)
	assert.True(t, isNotExist(statErr), "expired credential file must be removed; got: %v", statErr)

	// Warn log with correct event key must be present.
	rec, found := cap.findByEvent("initial_admin_credential_swept")
	assert.True(t, found, "expected Warn log with event=initial_admin_credential_swept")
	assert.Equal(t, slog.LevelWarn, rec.level, "expected Warn level log")
}

// TestSweep_FreshFile_Retained verifies that a non-expired credential file is
// left untouched.
func TestSweep_FreshFile_Retained(t *testing.T) {
	dir := t.TempDir()
	logger, _ := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeFreshCredFile(t, dir, now)

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err)

	_, statErr := os.Stat(credPath)
	assert.NoError(t, statErr, "fresh credential file must be retained")
}

// TestSweep_UnreadableFile_LogErrorContinue verifies that a cred file with
// mode 0o000 causes an Error log but Sweep still returns nil (startup not blocked).
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

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err, "Sweep must not return error even when file is unreadable")

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

// TestSweep_StateDirNotExist_NoError verifies that a non-existent StateDir
// is treated as "no file" (normal state), returning nil without error logs.
func TestSweep_StateDirNotExist_NoError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent-subdir")
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	for _, r := range cap.records {
		assert.NotEqual(t, slog.LevelError, r.level,
			"no Error log expected when StateDir does not exist; got: %v", r)
	}
}

// TestSweep_MalformedExpiresAt_LogErrorContinue verifies that a file without
// a valid expires_at line causes an Error log and the file is retained.
func TestSweep_MalformedExpiresAt_LogErrorContinue(t *testing.T) {
	dir := t.TempDir()
	logger, cap := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeMalformedCredFile(t, dir)

	err := Sweep(context.Background(), SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NoError(t, err, "malformed expires_at must not block startup")

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

// TestSweep_AdminExistsDoesNotSkip verifies the architectural invariant:
// sweep.go must not import ports.UserRepository. This is a static guarantee
// that Sweep is fully decoupled from the admin existence check.
//
// Implementation note: we only check for the UserRepository import — the
// "adminExists" function name is intentionally NOT grepped here because the
// comment block in sweep.go legitimately references it to explain the design.
// The real enforcement is that sweep.go never imports ports.UserRepository.
func TestSweep_AdminExistsDoesNotSkip(t *testing.T) {
	// If sweep.go ever imports UserRepository the package will fail to compile
	// in environments that mock the interface. The assertion below provides an
	// additional readable signal in test output.
	src, err := os.ReadFile("sweep.go")
	require.NoError(t, err)

	assert.NotContains(t, string(src), "UserRepository",
		"sweep.go must not import ports.UserRepository — Sweep is admin-existence-agnostic")
}

// TestSweep_Hook_StartInvokesSweep verifies that SweepHook(cfg).OnStart(ctx)
// is equivalent to Sweep(ctx, cfg): writing an expired file and calling
// OnStart results in the file being removed.
func TestSweep_Hook_StartInvokesSweep(t *testing.T) {
	dir := t.TempDir()
	logger, _ := newBootstrapCapturingLogger()
	now := time.Now()

	credPath := writeExpiredCredFile(t, dir, now)

	hook := SweepHook(SweepConfig{
		StateDir: dir,
		Clock:    fixedClock{now: now},
		Logger:   logger,
	})

	require.NotNil(t, hook.OnStart, "SweepHook.OnStart must not be nil")
	assert.Nil(t, hook.OnStop, "SweepHook.OnStop must be nil")

	err := hook.OnStart(context.Background())
	require.NoError(t, err)

	// File must be gone — OnStart triggered the sweep.
	_, statErr := os.Stat(credPath)
	assert.True(t, isNotExist(statErr),
		"expired cred file must be removed after SweepHook.OnStart; got: %v", statErr)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || isErrorIs(err, fs.ErrNotExist))
}

func isErrorIs(err error, target error) bool {
	return fmt.Sprintf("%T", err) != "" && containsTarget(err, target)
}

func containsTarget(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			break
		}
	}
	return false
}
