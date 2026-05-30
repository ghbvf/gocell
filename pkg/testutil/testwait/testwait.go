// Package testwait provides typed test-wait markers that replace bare
// require.Eventually / assert.Eventually polling. Two semantics are exposed
// as separate functions (typed function choice):
//
//   - External: polling REQUIRED because no channel signal is producible
//     (HTTP /healthz, container readiness, subprocess signal handler).
//     Caller MUST provide a const-literal kebab-case reason documenting
//     why polling is unavoidable. Downstream form-uniqueness is locked by
//     archtest TEST-POLLING-EXTERNAL-REASON-LITERAL-01 ((callee, arg)
//     uniqueness on testwait.External); upstream form-uniqueness is locked
//     by sibling archtest TEST-EVENTUALLY-FUNNEL-01 (bans bare
//     require.Eventually / assert.Eventually / *WithT across the module).
//     Together they form a Hard funnel per ai-robust.md §"Funnel 双向锁评级"
//     and Hard 范本 "typed marker funnel for unbounded ops" (sibling of
//     panicregister.Approved).
//   - Deterministic: blocks on a channel signal — no polling, no race window.
//     The default choice; use External only as carve-out. Hard via Go type
//     system: <-chan T signature makes "polling via Deterministic"
//     unrepresentable. The internal safety-net timeout is [signalSafetyNet];
//     callers do NOT pass a timeout — the signal arrival IS the
//     synchronization point, so any caller-supplied timeout would be a latency
//     budget, not a hung-test guard.
//
// Polling is the leading source of race-CI flakes in GoCell tests; see
// docs/plans/202605181600-042-archtest.md §1.1 (TEST-POLLING-DETERMINISM).
//
// ref: stretchr/testify pull/1657 (synchronous Eventually proposal — root of
//
//	External's synchronous-poll design, eliminating testify #1611 goroutine
//	leak and #865 race window)
//
// ref: kubernetes/apimachinery pkg/util/wait/loop.go (synchronous main loop
//
//	with explicit ctx.Err checks between ticks)
//
// ref: pkg/panicregister/panicregister.go (typed-marker funnel sibling pattern)
package testwait

import (
	"fmt"
	"time"
)

// signalSafetyNet bounds Deterministic's wait on a guaranteed signal.
// It is a hung-test guard, NOT a latency budget — a correct run receives
// the signal effectively instantly, so this is never hit in a green run
// even under the race detector. Sized comfortably below `go test -timeout`
// so a deadlocked signal fails readably here instead of as a whole-suite kill.
const signalSafetyNet = 30 * time.Second

// TB is the testing surface required by External and Deterministic. It
// matches the subset of testing.TB the helpers actually use, so test code
// that supplies a spy implementation (e.g. for self-tests that need to
// observe Fatalf without aborting) can do so without depending on every
// method of testing.TB.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// External polls condition until it returns true or timeout fires.
//
// Note: parameter order is (t, reason, condition, ...) which DIFFERS from
// testify's (t, condition, ...). reason precedes condition by design — see
// archtest TEST-POLLING-EXTERNAL-REASON-LITERAL-01 for the rationale: the
// reason must be a visible const literal at the callsite, placing it as the
// second argument (immediately after t) makes it hard to omit accidentally
// and easy to find when grep-scanning CI failure output.
//
// reason MUST be a const string literal in kebab-case form (matching
// ^[a-z][a-z0-9-]+$); archtest TEST-POLLING-EXTERNAL-REASON-LITERAL-01
// rejects any other form (variable, fmt.Sprintf, concatenation, const ident).
//
// Polling runs synchronously on the caller goroutine. No per-tick goroutine
// is spawned — this eliminates the goroutine leak of testify Eventually
// (stretchr/testify#1611) and the closure-capture race window
// (stretchr/testify#865 / #835) that previously caused race-CI flakes
// (D500ms incident).
//
// On timeout the function calls t.Fatalf with the reason and the supplied
// msgAndArgs. On condition panic the panic propagates to the caller — it
// is not recovered, mirroring `require.Eventually`'s synchronous main-thread
// runner semantics from testify pull/1657.
//
// tick should be < timeout to ensure at least one poll occurs before the
// timeout fires.
//
// Prefer Deterministic whenever a channel signal is producible from the
// system under test.
func External(t TB, reason string, condition func() bool,
	timeout, tick time.Duration, msgAndArgs ...any,
) {
	t.Helper()
	// Initial check before the first tick so a condition that is already
	// true returns immediately (mirrors k8s wait.PollUntilContextTimeout
	// immediate=true semantics).
	if condition() {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("testwait.External: timeout after %v waiting for %q: %s",
				timeout, reason, formatMsgAndArgs(msgAndArgs))
			return
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

// Deterministic blocks on signal until it receives a value or the internal
// safety-net timeout fires. On success returns the received value; on safety-net
// expiry calls t.Fatalf and returns the zero value of T.
//
// The timeout is NOT caller-configurable. Deterministic is designed for signals
// that are guaranteed to arrive in a correct run; the internal [signalSafetyNet]
// is a hung-test guard only. A caller-supplied timeout would be a latency budget
// (the root cause of race-CI flakes in observer_test.go, issue #1320). If you
// genuinely need a poll with a configurable timeout, use External instead.
//
// This is the polling-free wait: the channel signal IS the synchronization
// point. No closure capture, no race window with concurrent producers — Go's
// channel happens-before guarantee replaces the manual polling required by
// External.
//
// Hard via Go type system: signal's <-chan T signature lets callers receive
// any payload type but forbids "polling via Deterministic" — the API name
// and signature together pin the channel-blocking semantics.
//
// For CI grep-ability, pass a short label string as the first msgAndArg:
//
//	testwait.Deterministic(t, sig, "session-created")
//
// The label appears verbatim in the t.Fatalf output, making the timeout
// site findable via `grep "session-created" ci.log`.
func Deterministic[T any](t TB, signal <-chan T, msgAndArgs ...any) T {
	t.Helper()
	return deterministicWithin(t, signal, signalSafetyNet, msgAndArgs...)
}

// deterministicWithin is the implementation behind Deterministic. budget is an
// INTERNAL hung-test guard, never a caller-facing knob — Deterministic always
// passes signalSafetyNet. It is split out so the white-box self-test
// (export_test.go) can exercise the safety-net-expiry branch with a short
// budget instead of waiting the full signalSafetyNet. Business test code in
// other packages cannot reach it: the export_test.go forwarder is visible only
// inside testwait's own test binary, so the timeout-free public funnel holds.
func deterministicWithin[T any](t TB, signal <-chan T, budget time.Duration, msgAndArgs ...any) T {
	t.Helper()
	var zero T
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case v := <-signal:
		return v
	case <-timer.C:
		t.Fatalf("testwait.Deterministic: safety-net expired after %v waiting on signal: %s",
			budget, formatMsgAndArgs(msgAndArgs))
		return zero
	}
}

// formatMsgAndArgs renders the variadic message tail. Layout mirrors
// testify's familiar (format, args...) convention but resolves to a plain
// string so it can be embedded in t.Fatalf's own format string without a
// second pass through fmt.
func formatMsgAndArgs(msgAndArgs []any) string {
	switch len(msgAndArgs) {
	case 0:
		return ""
	case 1:
		if s, ok := msgAndArgs[0].(string); ok {
			return s
		}
		return fmt.Sprint(msgAndArgs[0])
	default:
		if format, ok := msgAndArgs[0].(string); ok {
			return fmt.Sprintf(format, msgAndArgs[1:]...)
		}
		return fmt.Sprint(msgAndArgs...)
	}
}
