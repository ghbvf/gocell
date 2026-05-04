//go:build windows

package shutdown

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestSignalsToWatch_Windows asserts the Windows signal set is exactly
// [os.Interrupt]. Windows cannot deliver SIGTERM from outside the
// process; service-controller stop events (SERVICE_CONTROL_STOP) are
// out of scope here. Locking the set in a test prevents an accidental
// regression that adds syscall.SIGTERM (which is silently ignored on
// Windows and leaves shutdown half-broken).
func TestSignalsToWatch_Windows(t *testing.T) {
	got := signalsToWatch()
	require.Len(t, got, 1, "Windows must watch exactly one signal (os.Interrupt)")
	assert.Equal(t, os.Interrupt, got[0], "Windows signal must be os.Interrupt — SIGTERM is undeliverable")
}

// TestNotifyContext_Windows_RegistersInterrupt verifies NotifyContext
// wires up Windows interrupt delivery without panicking. Actually
// raising os.Interrupt requires GenerateConsoleCtrlEvent against an
// attached console, which is unreliable in CI; this test instead
// exercises the registration + cancel path that's portable.
func TestNotifyContext_Windows_RegistersInterrupt(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	ctx, cancel := NotifyContext(parent)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("context should not be done immediately after NotifyContext")
	case <-time.After(testtime.MediumPoll):
	}

	// Cancel via parent — the only portable way to drive the Windows path
	// without console-event plumbing — and assert ctx propagates the cancel.
	parentCancel()
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("context did not become done after parent cancel")
	}
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}
