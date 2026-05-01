package cmdrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestRunIn(t *testing.T) {
	tool, err := NewTool(goName())
	require.NoError(t, err)

	t.Run("inherits parent env when env nil", func(t *testing.T) {
		out, err := RunIn(context.Background(), tool, "", nil, "version")
		require.NoError(t, err)
		assert.Contains(t, string(out), "go version")
	})

	t.Run("inherits parent dir when dir empty", func(t *testing.T) {
		out, err := RunIn(context.Background(), tool, "", nil, "version")
		require.NoError(t, err)
		assert.NotEmpty(t, out)
	})

	t.Run("respects ctx deadline", func(t *testing.T) {
		// Pre-expired deadline forces abort before fork.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := RunIn(ctx, tool, "", nil, "env")
		require.Error(t, err)
	})
}

func goName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}
