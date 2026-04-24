package shutdown

import (
	"context"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyContext_CancelStopsContext(t *testing.T) {
	parent := context.Background()
	ctx, cancel := NotifyContext(parent)
	defer cancel()

	// Context must not be done yet.
	select {
	case <-ctx.Done():
		t.Fatal("context should not be done before cancel is called")
	default:
	}

	// Calling cancel must close the context.
	cancel()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("context did not become done after cancel()")
	}

	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestNotifyContext_InterruptCancelsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.Kill(SIGINT) is not available on Windows; signal delivery tested manually")
	}

	parent := context.Background()
	ctx, cancel := NotifyContext(parent)
	defer cancel()

	proc, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)

	// Give the signal handler a moment to register.
	time.Sleep(10 * time.Millisecond)

	// Send SIGINT to self.
	require.NoError(t, proc.Signal(syscall.SIGINT))

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(3 * time.Second):
		t.Fatal("context did not become done after SIGINT")
	}

	assert.Error(t, ctx.Err())
}
