// Package testwait provides typed test-wait markers that replace bare
// require.Eventually / assert.Eventually polling. Two semantics are exposed
// as separate functions (typed function choice):
//
//   - External: polling REQUIRED because no channel signal is producible
//     (HTTP /healthz, container readiness, subprocess signal handler).
//     Caller MUST provide a const-literal kebab-case reason documenting
//     why polling is unavoidable. archtest TEST-POLLING-EXTERNAL-REASON-
//     LITERAL-01 (Hard, downstream funnel) enforces the (callee, arg) form.
//   - Deterministic: blocks on a channel signal with timeout — no polling,
//     no race window. The default choice; use External only as carve-out.
//     Hard via Go type system: <-chan T signature makes "polling via
//     Deterministic" unrepresentable.
//
// Polling is the leading source of race-CI flakes in GoCell tests; see
// docs/plans/202605181600-042-archtest.md §1.1 (TEST-POLLING-DETERMINISM).
//
// Upstream funnel closure (banning bare require.Eventually / assert.Eventually)
// is deferred to PR3 per the plan above (TEST-EVENTUALLY-FUNNEL-01). Until
// then this funnel is "Hard downstream + Soft upstream" transitional. AI-rebust
// §"Funnel 双向锁评级" permits this with backlog registration; the registration
// anchor is plan §1.1 PR3 — see .claude/rules/gocell/ai-collab.md.
// Upstream funnel closure backlog anchor: TEST-EVENTUALLY-FUNNEL-01 (registered
// in docs/plans/202605181600-042-archtest.md §1.1 PR3).
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
// Prefer Deterministic whenever a channel signal is producible from the
// system under test.
func External(t TB, reason string, condition func() bool,
	timeout, tick time.Duration, msgAndArgs ...any,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	// Initial check before the first tick so a condition that is already
	// true returns immediately (mirrors k8s wait.PollUntilContextTimeout
	// immediate=true semantics).
	if condition() {
		return
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for range ticker.C {
		if condition() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("testwait.External: timeout after %v waiting for %q: %s",
				timeout, reason, formatMsgAndArgs(msgAndArgs))
			return
		}
	}
}

// Deterministic blocks on signal until it receives a value or timeout fires.
// On success returns the received value; on timeout calls t.Fatalf and
// returns the zero value of T.
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
//	testwait.Deterministic(t, sig, testtime.EventuallyShort, "session-created")
//
// The label appears verbatim in the t.Fatalf output, making the timeout
// site findable via `grep "session-created" ci.log`.
func Deterministic[T any](t TB, signal <-chan T, timeout time.Duration,
	msgAndArgs ...any,
) T {
	t.Helper()
	var zero T
	select {
	case v := <-signal:
		return v
	case <-time.After(timeout):
		t.Fatalf("testwait.Deterministic: timeout after %v waiting on signal: %s",
			timeout, formatMsgAndArgs(msgAndArgs))
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
