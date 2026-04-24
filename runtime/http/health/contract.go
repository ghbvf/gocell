package health

import (
	"context"
	"testing"
	"time"
)

// CheckCtxRespected is a test-only helper that verifies a raw Checker
// cooperates with ctx cancellation. Probe authors can drop this into their
// unit tests to detect the class of bugs that PR-A35's wrapCtxSafe papers
// over at runtime: an inner fn that ignores ctx leaves a goroutine behind on
// every /readyz invocation, which — while harmless to the aggregator — is
// still a correctness issue worth catching at development time.
//
// The helper calls fn with an already-cancelled ctx and asserts that fn
// returns within the supplied budget. budget is the only argument so that
// individual tests can pick a value appropriate to their CI environment; the
// runtime path in Handler does not depend on this value at all.
//
// If fn does not return within budget, the helper calls t.Errorf with a
// descriptive message but does not call t.Fatal, so the surrounding test can
// decide whether to continue exercising other checkers.
func CheckCtxRespected(t testing.TB, fn Checker, budget time.Duration) {
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
