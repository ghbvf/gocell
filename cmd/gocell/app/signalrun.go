package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// signalGraceWindow bounds how long a ctx-ignoring sub-command may keep
// running after the first SIGINT/SIGTERM before the process is force-
// terminated.
//
// Why a watchdog is needed: main.go installs a process-global
// signal.NotifyContext, which suppresses Go's default "SIGINT terminates
// the process" disposition for EVERY command. ctx-aware commands
// (validate, verify slice/cell/journey/generated) observe the canceled ctx
// and return promptly, so Dispatch renders "interrupted"/ExitRuntime well
// within this window (validate re-checks ctx per rule; verify kills the
// `go test` subprocess via pkg/cmdrun process-group kill). But the
// ctx-ignoring commands — graph (depgraph.Load wraps a non-ctx-native
// go/packages load), export, check, scaffold, verify codegen-* (worktree
// sandbox is not ctx-native) — would otherwise swallow Ctrl+C entirely and
// run to completion (even exit 0), a regression from the pre-signal-wiring
// "instant kill". The watchdog restores a bounded equivalent: a single
// Ctrl+C always terminates within signalGraceWindow regardless of whether
// the running command honors ctx.
//
// 2s keeps an uninterruptible command responsive to Ctrl+C while leaving
// ample headroom for a ctx-aware command's graceful unwind (which is
// near-instant).
const signalGraceWindow = 2 * time.Second

// RunWithSignal is the cmd/gocell/main.go entry point. It wires
// SIGINT/SIGTERM to ctx cancellation and the bounded-termination watchdog,
// then dispatches args. See runWithSignal for the mechanism; the seam
// exists so the three branches (no-signal / ctx-aware graceful /
// ctx-ignoring watchdog) are deterministically unit-testable without real
// signals or os.Exit.
func RunWithSignal(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return runWithSignal(ctx, stop, signalGraceWindow, args, Dispatch, os.Exit)
}

// runWithSignal runs dispatch(ctx, args) and guarantees the process
// terminates within a bounded window on signal:
//
//   - no signal: dispatch returns, the watchdog goroutine observes `done`
//     and exits cleanly (no goroutine leak, no spurious forceExit).
//   - signal + ctx-aware command: ctx is canceled, the default signal
//     disposition is restored via stop() (a second Ctrl+C now hard-kills
//     immediately), the command unwinds and returns within grace; `done`
//     closes first so forceExit is NOT called and runWithSignal returns
//     the command's exit code (Dispatch maps context.Canceled to the
//     "interrupted" line + ExitRuntime).
//   - signal + ctx-ignoring command: the command does not return within
//     grace → forceExit(130) terminates the process (128+SIGINT: the
//     process is being signal-terminated).
//
// forceExit is os.Exit in production; injected in tests. stop is
// idempotent (signal.NotifyContext's CancelFunc), so the multiple calls
// here are safe.
func runWithSignal(
	ctx context.Context,
	stop context.CancelFunc,
	grace time.Duration,
	args []string,
	dispatch func(context.Context, []string) int,
	forceExit func(int),
) int {
	defer stop()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// First signal: re-arm Go's default disposition so a second
			// Ctrl+C hard-kills immediately even if this command ignores
			// ctx, then bound the first-signal wait.
			stop()
			select {
			case <-time.After(grace):
				forceExit(130)
			case <-done:
				// Command unwound within grace (ctx-aware) — no force kill.
			}
		case <-done:
			// Command finished with no signal — clean watchdog exit.
		}
	}()
	code := dispatch(ctx, args)
	close(done)
	stop()
	return code
}
