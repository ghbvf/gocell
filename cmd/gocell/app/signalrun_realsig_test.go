package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Real-OS-signal end-to-end coverage for the signal wiring (PR #502
// review: "补 …SIGINT 行为测试"). The deterministic branch logic is in
// signalrun_test.go; this proves the same logic survives a REAL
// signal.NotifyContext + a REAL SIGINT + the real os.Exit path.
//
// Why re-exec-self and not a real gocell command: empirically every warm
// gocell sub-command finishes in well under 2s, so a subprocess that runs
// one and races a timed SIGINT is inherently flaky (the project forbids
// fragile constructs). The child here instead enters the production signal
// wiring with a dispatch that blocks until the signal arrives, so the
// signal delivery is race-free regardless of machine speed. This is the
// same self-re-exec pattern Go's own os/signal tests use.
//
// envSignalE2EMode selects child behavior; empty = parent (driver).
const envSignalE2EMode = "GOCELL_SIGNAL_E2E_MODE"

// TestSignalRealE2E forks the test binary back into itself in "child"
// mode, where the child runs the real signal.NotifyContext +
// runWithSignal path with an injected dispatch, then the parent sends a
// real SIGINT and asserts the child's process exit:
//
//   - watchdog: a ctx-IGNORING dispatch that blocks forever — the real
//     watchdog must force-exit the process with 130 (review Point 2).
//   - graceful: a ctx-AWARE dispatch that returns on cancel — the process
//     must exit ExitRuntime(1), not 130 and not 0 (review Point 1 shape).
func TestSignalRealE2E(t *testing.T) {
	if mode := os.Getenv(envSignalE2EMode); mode != "" {
		signalE2EChild(mode) // never returns (os.Exit)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("relies on POSIX SIGINT semantics")
	}

	cases := []struct {
		mode     string
		wantCode int
	}{
		{"watchdog", 130}, // 128+SIGINT: ctx-ignoring command force-killed
		{"graceful", 1},   // ExitRuntime: ctx-aware command unwound
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			// os.Args[0] is this test binary (self re-exec, the Go
			// stdlib os/signal test pattern); not user input.
			cmd := exec.Command(os.Args[0], //nolint:gosec // self re-exec of the test binary; args are literals
				"-test.run=^TestSignalRealE2E$", "-test.count=1")
			cmd.Env = append(os.Environ(), envSignalE2EMode+"="+tc.mode)
			if err := cmd.Start(); err != nil {
				t.Fatalf("re-exec child: %v", err)
			}

			var waitErr error
			done := make(chan struct{})
			go func() { waitErr = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-done
			})

			// The child's FIRST action is signal.NotifyContext, then it
			// blocks. 1s is far past child process+test-framework startup
			// and the child cannot finish early (it blocks until signaled),
			// so this is race-free, not a timing guess.
			time.Sleep(1 * time.Second)
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatalf("send SIGINT to child: %v", err)
			}

			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("child did not exit within 15s of SIGINT (mode=%s)", tc.mode)
			}

			code := exitCodeOf(waitErr)
			if code != tc.wantCode {
				t.Fatalf("mode=%s: child exit code = %d, want %d",
					tc.mode, code, tc.wantCode)
			}
		})
	}
}

// signalE2EChild runs the production signal wiring (real
// signal.NotifyContext, real runWithSignal, real os.Exit) with a dispatch
// chosen by mode, then exits. It never returns.
func signalE2EChild(mode string) {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	var (
		grace    = signalGraceWindow
		dispatch func(context.Context, []string) int
	)
	switch mode {
	case "watchdog":
		// Short grace keeps the test fast; the point is that a
		// ctx-ignoring command (blocks forever) is force-terminated.
		grace = 400 * time.Millisecond
		dispatch = func(context.Context, []string) int { select {} }
	case "graceful":
		dispatch = func(c context.Context, _ []string) int {
			<-c.Done()
			return ExitRuntime
		}
	default:
		os.Exit(99) // unknown mode — fail loudly in the parent assertion
	}
	os.Exit(runWithSignal(ctx, stop, grace, nil, dispatch, os.Exit))
}

// exitCodeOf extracts a process exit code from cmd.Wait's error.
// nil → 0; *exec.ExitError → its code; anything else → -1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
