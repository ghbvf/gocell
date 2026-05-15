package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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
// wiring with a dispatch that blocks until the signal arrives, and prints
// a readiness marker once its handler is installed so the parent delivers
// the signal on a handshake (no sleep, race-free regardless of machine
// speed). This is the same self-re-exec pattern Go's own os/signal tests
// use.
const (
	envSignalE2EMode = "GOCELL_SIGNAL_E2E_MODE"
	signalChildReady = "SIGNAL_CHILD_READY"
	// childWatchdogGrace keeps the watchdog-mode child fast; a named const
	// (not an inline duration literal) per the test-time-literal rule.
	childWatchdogGrace = testtime.D500ms
)

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
			// os.Args[0] is this test binary (self re-exec, the Go stdlib
			// os/signal test pattern); not user input.
			cmd := exec.Command(os.Args[0], //nolint:gosec // self re-exec of the test binary; args are literals
				"-test.run=^TestSignalRealE2E$", "-test.count=1")
			cmd.Env = append(os.Environ(), envSignalE2EMode+"="+tc.mode)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("stdout pipe: %v", err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatalf("re-exec child: %v", err)
			}

			// Drain child stdout; signal `ready` when the handshake marker
			// is seen. Reading blocks on the pipe (no sleep) and continues
			// to EOF so cmd.Wait never deadlocks on a full pipe.
			ready := make(chan struct{})
			go func() {
				sc := bufio.NewScanner(stdout)
				seen := false
				for sc.Scan() {
					if !seen && sc.Text() == signalChildReady {
						seen = true
						close(ready)
					}
				}
				if !seen {
					// Child died before printing the marker; unblock the
					// parent so it fails on the exit-code assertion with
					// context rather than hanging.
					close(ready)
				}
				_, _ = io.Copy(io.Discard, stdout)
			}()

			var waitErr error
			done := make(chan struct{})
			go func() { waitErr = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-done
			})

			select {
			case <-ready:
			case <-time.After(testtime.EventuallyExtraLong):
				t.Fatalf("child never reported %q (mode=%s)", signalChildReady, tc.mode)
			}

			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatalf("send SIGINT to child: %v", err)
			}

			select {
			case <-done:
			case <-time.After(testtime.CtxLong):
				t.Fatalf("child did not exit within timeout of SIGINT (mode=%s)", tc.mode)
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
// chosen by mode. It prints the readiness marker once the signal handler
// is installed, then never returns.
func signalE2EChild(mode string) {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	var (
		grace    = signalGraceWindow
		dispatch func(context.Context, []string) int
	)
	switch mode {
	case "watchdog":
		grace = childWatchdogGrace
		dispatch = func(context.Context, []string) int { select {} }
	case "graceful":
		dispatch = func(c context.Context, _ []string) int {
			<-c.Done()
			return ExitRuntime
		}
	default:
		os.Exit(99) // unknown mode — fail loudly in the parent assertion
	}
	// Handshake: the handler is registered above; tell the parent it is
	// safe to deliver the signal now (race-free, no sleep).
	fmt.Println(signalChildReady)
	os.Exit(runWithSignal(ctx, stop, clock.Real(), grace, nil, dispatch, os.Exit))
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
