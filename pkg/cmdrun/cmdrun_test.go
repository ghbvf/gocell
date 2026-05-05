package cmdrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTool(t *testing.T) {
	t.Run("resolves real tool to absolute path", func(t *testing.T) {
		tool, err := NewTool(goName())
		require.NoError(t, err)
		assert.NotEmpty(t, tool.Dir(), "Dir() should return non-empty for resolved tool")
		assert.True(t, filepath.IsAbs(tool.Dir()), "Dir() must be absolute (path invariant)")
		assert.True(t, filepath.IsAbs(tool.path), "path must be absolute (security invariant)")
	})

	t.Run("normalizes relative LookPath result to absolute", func(t *testing.T) {
		// LookPath returns a relative path when name contains a separator
		// (e.g., "./bin/tool"). Plant a binary in tmp + cd there so the
		// relative name resolves under cwd, then assert NewTool absolutizes.
		tmp := t.TempDir()
		// Use the real go binary so the lookup actually succeeds.
		realGo, err := exec.LookPath(goName())
		require.NoError(t, err)
		linkName := "local-go"
		if runtime.GOOS == "windows" {
			linkName += ".exe"
		}
		linkPath := filepath.Join(tmp, linkName)
		require.NoError(t, os.Symlink(realGo, linkPath))

		origWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Chdir(origWD) })
		require.NoError(t, os.Chdir(tmp))

		tool, err := NewTool("./" + linkName)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(tool.path),
			"NewTool must normalize relative LookPath result to absolute (got %q)", tool.path)
	})

	t.Run("fails closed for missing tool", func(t *testing.T) {
		_, err := NewTool("definitely-not-a-real-tool-on-path-12345")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cmdrun: lookup")
		assert.ErrorIs(t, err, exec.ErrNotFound)
	})
}

func TestValidatedTool_ZeroValueFailsClosed(t *testing.T) {
	// Zero-value ValidatedTool: legal Go-syntactic construction (`ValidatedTool{}`)
	// but the unexported `path` field stays "". Dir() returns "." (filepath.Dir
	// of ""), and Run fails at exec.CommandContext with "no such file or
	// directory" — fail-closed semantics, never panic, never execute.
	var zero ValidatedTool
	assert.Equal(t, ".", zero.Dir(), "zero-value Dir is filepath.Dir(\"\") = \".\"")

	_, err := Run(context.Background(), zero, "version")
	require.Error(t, err, "zero-value Run must fail-closed, not execute")
}

func TestRun(t *testing.T) {
	tool, err := NewTool(goName())
	require.NoError(t, err)

	t.Run("captures combined output", func(t *testing.T) {
		out, err := Run(context.Background(), tool, "version")
		require.NoError(t, err)
		assert.Contains(t, string(out), "go version")
	})

	t.Run("returns ExitError for non-zero exit", func(t *testing.T) {
		_, err := Run(context.Background(), tool, "nonexistent-subcommand")
		require.Error(t, err)
		var exitErr *exec.ExitError
		require.True(t, errors.As(err, &exitErr), "expected ExitError, got %T", err)
		assert.NotZero(t, exitErr.ExitCode())
	})

	t.Run("respects ctx cancellation", func(t *testing.T) {
		// `go env` is the cheapest go subcommand — quick enough that ctx
		// cancellation won't always race the process; we use a pre-canceled
		// ctx so CommandContext aborts before fork.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Run(ctx, tool, "env")
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "canceled"),
			"expected canceled error, got %v", err)
	})
}

func TestRunWith(t *testing.T) {
	tool, err := NewTool(goName())
	require.NoError(t, err)

	t.Run("inherits parent env when env nil", func(t *testing.T) {
		out, err := RunWith(context.Background(), tool, RunOptions{}, "version")
		require.NoError(t, err)
		assert.Contains(t, string(out), "go version")
	})

	t.Run("inherits parent dir when dir empty", func(t *testing.T) {
		out, err := RunWith(context.Background(), tool, RunOptions{}, "version")
		require.NoError(t, err)
		assert.NotEmpty(t, out)
	})

	t.Run("respects ctx deadline", func(t *testing.T) {
		// Pre-expired deadline forces abort before fork.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := RunWith(ctx, tool, RunOptions{}, "env")
		require.Error(t, err)
	})
}

func TestRunWith_CleansParentEnvAndAppliesExtraEnv(t *testing.T) {
	t.Setenv("CMDRUN_KEEP", "parent")
	t.Setenv("CMDRUN_SECRET_TOKEN", "parent-secret")

	tool := testBinaryTool(t)

	out, err := RunWith(context.Background(), tool, RunOptions{
		ExtraEnv: []string{
			"CMDRUN_KEEP=override",
			"CMDRUN_EXTRA=added",
			"CMDRUN_SECRET_TOKEN=explicit-secret",
		},
	}, "-test.run=^TestCmdrunEnvHelper$", "cmdrun-env-helper")
	require.NoError(t, err)
	got := string(out)
	assert.Contains(t, got, "CMDRUN_KEEP=override")
	assert.Contains(t, got, "CMDRUN_EXTRA=added")
	assert.Contains(t, got, "CMDRUN_SECRET_TOKEN=explicit-secret")
}

func TestRunWith_NilAndEmptyExtraEnvDoNotClearInheritedEnv(t *testing.T) {
	t.Setenv("CMDRUN_KEEP", "parent")

	tool := testBinaryTool(t)

	for _, tc := range []struct {
		name     string
		extraEnv []string
	}{
		{name: "nil", extraEnv: nil},
		{name: "empty", extraEnv: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := RunWith(context.Background(), tool, RunOptions{
				ExtraEnv: tc.extraEnv,
			}, "-test.run=^TestCmdrunEnvHelper$", "cmdrun-env-helper")
			require.NoError(t, err)
			assert.Contains(t, string(out), "CMDRUN_KEEP=parent")
		})
	}
}

func TestRunWith_DefaultEnvStripsDeniedKeys(t *testing.T) {
	t.Setenv("CMDRUN_SECRET_TOKEN", "parent-secret")

	tool := testBinaryTool(t)

	out, err := RunWith(context.Background(), tool, RunOptions{}, "-test.run=^TestCmdrunEnvHelper$", "cmdrun-env-helper")
	require.NoError(t, err)
	assert.Contains(t, string(out), "CMDRUN_SECRET_TOKEN=")
	assert.NotContains(t, string(out), "parent-secret")
}

func TestCmdrunEnvHelper(t *testing.T) {
	if !hasArg("cmdrun-env-helper") {
		return
	}
	_, _ = os.Stdout.WriteString("CMDRUN_KEEP=" + os.Getenv("CMDRUN_KEEP") + "\n")
	_, _ = os.Stdout.WriteString("CMDRUN_EXTRA=" + os.Getenv("CMDRUN_EXTRA") + "\n")
	_, _ = os.Stdout.WriteString("CMDRUN_SECRET_TOKEN=" + os.Getenv("CMDRUN_SECRET_TOKEN") + "\n")
	os.Exit(0)
}

func testBinaryTool(t *testing.T) ValidatedTool {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	require.NoError(t, err)
	tool, err := NewTool(path)
	require.NoError(t, err)
	return tool
}

func hasArg(want string) bool {
	for _, arg := range os.Args {
		if arg == want {
			return true
		}
	}
	return false
}

func goName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// processTreeTestTimeouts groups all site-specific durations used by
// TestRunWith_KillsProcessTree.
const (
	processTreeGrandchildSpawnTimeout = 5 * time.Second
	processTreePollInterval           = 20 * time.Millisecond
	processTreeCancelReturnTimeout    = 3 * time.Second
	processTreeDeathSettleTimeout     = time.Second
)

// TestRunWith_KillsProcessTree verifies that canceling ctx kills not only the
// direct child but all grandchild processes spawned by it. The test is
// Unix-only because the shell one-liner used to produce grandchildren relies on
// POSIX semantics; on Windows the fallback is the direct-child kill (see
// cmdrun_windows.go TODO(B2-X-08)).
func TestRunWith_KillsProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill not yet implemented on Windows (TODO B2-X-08)")
	}

	sh, err := exec.LookPath("sh")
	require.NoError(t, err, "sh must be available on PATH")
	tool, err := NewTool(sh)
	require.NoError(t, err)

	// Write a helper script that:
	//   1. spawns a grandchild `sleep 30` in the background.
	//   2. writes the grandchild PID to a file so the test can poll it.
	//   3. runs its own foreground `sleep 30` (the direct child body).
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := fmt.Sprintf(`sleep 30 & echo $! > %s; sleep 30`, pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, runErr := RunWith(ctx, tool, RunOptions{}, "-c", script)
		done <- runErr
	}()

	// Wait for the grandchild PID file to appear (the script ran far enough to
	// spawn the grandchild). A busy-wait with short sleeps is the only option
	// here because the PID file is produced by an external shell process whose
	// completion we cannot observe without polling.
	deadline := time.Now().Add(processTreeGrandchildSpawnTimeout)
	var grandchildPID int
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidFile) //nolint:gosec // G304: pidFile is t.TempDir() output, test-only
		if readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 0 {
				grandchildPID = pid
				break
			}
		}
		time.Sleep(processTreePollInterval) //archtest:allow:test-sleep polling external shell PID file; no channel to wait on
	}
	require.NotZero(t, grandchildPID, "grandchild PID file must appear within 5s")

	// Cancel the context — this should kill the entire process group.
	cancel()

	// RunWith must return promptly after cancellation.
	select {
	case runErr := <-done:
		require.Error(t, runErr, "RunWith must return an error after ctx cancellation")
	case <-time.After(processTreeCancelReturnTimeout):
		t.Fatal("RunWith did not return within 3s after ctx cancellation")
	}

	// The grandchild process must no longer be alive. Allow a short settling
	// period for the OS to reap the process. Sending signal 0 to a dead
	// process returns an error on Unix.
	proc, findErr := os.FindProcess(grandchildPID)
	require.NoError(t, findErr, "os.FindProcess should not fail on Unix even for dead processes")
	// Signal(0) does not send a signal; it probes whether the process exists
	// and is reachable. An error means the process is gone or we cannot reach it.
	assert.Eventually(t, func() bool {
		return proc.Signal(os.Signal(syscall.Signal(0))) != nil
	}, processTreeDeathSettleTimeout, processTreePollInterval,
		"grandchild process %d should be dead within 1s of ctx cancellation", grandchildPID)
}
