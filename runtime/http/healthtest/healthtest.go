// Package healthtest provides test-only helpers for probe authors writing
// against the runtime/http/health API. It is a thin, dependency-light
// sibling of runtime/http/health so `testing` never becomes a transitive
// production dependency of health.
//
// ref: net/http/httptest — the Go standard library's convention for keeping
// testing helpers out of the main production package so production binaries
// do not pull in the testing import graph.
package healthtest

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/http/health"
)

// CheckCtxRespected verifies that a raw health.Checker cooperates with
// ctx cancellation. Probe authors can drop this into their unit tests to
// detect the class of bugs that PR-A35's wrapCtxSafe papers over at
// runtime: an inner fn that ignores ctx leaves a goroutine behind on every
// /readyz invocation, which — while harmless to the aggregator — is still
// a correctness issue worth catching at development time.
//
// The helper calls fn with an already-canceled ctx and asserts that fn
// returns within the supplied budget. budget is the only argument so that
// individual tests can pick a value appropriate to their CI environment;
// the runtime path in health.Handler does not depend on this value at all.
//
// If fn does not return within budget, the helper calls t.Errorf with a
// descriptive message but does not call t.Fatal, so the surrounding test
// can decide whether to continue exercising other checkers.
//
// Goroutine lifetime: if fn ignores ctx, the goroutine launched by this
// helper will outlive the call. Callers whose test suites care about
// goroutine hygiene (goleak, parallel tests) must arrange for fn to exit
// on its own — typically by closing an external channel in t.Cleanup or
// wrapping fn in a probe that responds to an alternate signal. The helper
// intentionally does not spawn a secondary cancel goroutine because it
// cannot force fn to return.
func CheckCtxRespected(t testing.TB, fn health.Checker, budget time.Duration) {
	t.Helper()
	if fn == nil {
		t.Errorf("CheckCtxRespected: nil checker")
		return
	}
	if budget <= 0 {
		budget = 100 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_ = fn(ctx)
	}()
	select {
	case <-done:
		return
	case <-time.After(budget):
		t.Errorf("checker did not return within %s after ctx cancellation (elapsed: %s)",
			budget, time.Since(start))
	}
}
