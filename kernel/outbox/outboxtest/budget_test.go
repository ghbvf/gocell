package outboxtest

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Package-level duration constants for budget_test.go.
const (
	// testBudgetHangs is the close budget for the hanging-subscriber test.
	testBudgetHangs = testtime.D100ms

	// testBudgetAwait is the close budget for the awaitWithBudget timeout test.
	testBudgetAwait = testtime.D50ms

	// budgetMultiplier5 is used to check that elapsed time is less than 5x budget.
	budgetMultiplier5 = 5
)

// hangingCloseSubscriber.Close ignores ctx and blocks until the goroutine is
// torn down by process exit. Models a subscriber implementation that violates
// the Close(ctx) contract.
type hangingCloseSubscriber struct{}

func (hangingCloseSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (hangingCloseSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (hangingCloseSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	return nil
}

func (hangingCloseSubscriber) Close(_ context.Context) error {
	select {} // hang forever, ignoring ctx
}

// goexitCloseSubscriber.Close calls runtime.Goexit, which terminates the
// goroutine without ever sending on errCh. Without the defer sentinel in
// closeWithBudget the caller would block on the chan receive forever (the
// time.After branch only fires after `budget`, but more importantly the test
// here exercises that the sentinel routes the failure through errCh promptly).
type goexitCloseSubscriber struct{ hangingCloseSubscriber }

func (goexitCloseSubscriber) Close(_ context.Context) error {
	runtime.Goexit()
	return nil // unreachable
}

// panicCloseSubscriber.Close calls panic, simulating a misbehaving adapter
// whose Close panics. Without recover() in closeWithBudget this would crash
// the entire test binary; with it, the panic is caught and returned as an error.
type panicCloseSubscriber struct{ hangingCloseSubscriber }

func (panicCloseSubscriber) Close(_ context.Context) error {
	panic("boom")
}

// fastCloseSubscriber.Close returns the wrapped error immediately. Verifies
// the happy path passes the error through unchanged.
type fastCloseSubscriber struct {
	hangingCloseSubscriber
	err error
}

func (s fastCloseSubscriber) Close(_ context.Context) error { return s.err }

func TestCloseWithBudget_FailsWithinBudgetWhenCloseHangs(t *testing.T) {
	t.Parallel()
	const topic = "topic-hangs"

	start := time.Now()
	err := closeWithBudget(t, hangingCloseSubscriber{}, topic, testBudgetHangs)
	elapsed := time.Since(start)

	assertTrue(t, err != nil, "expected timeout error")
	if elapsed >= budgetMultiplier5*testBudgetHangs {
		t.Fatalf("closeWithBudget should return promptly after budget; took %s (budget=%s)", elapsed, testBudgetHangs)
	}
	assertTrue(t, strings.Contains(err.Error(), topic), "error must include topic identity, got: "+err.Error())
	assertTrue(t, strings.Contains(err.Error(), "goroutine leaked"),
		"error must declare the accepted leak, got: "+err.Error())
}

func TestCloseWithBudget_DefendsAgainstGoexitInClose(t *testing.T) {
	t.Parallel()
	const budget = time.Second

	start := time.Now()
	err := closeWithBudget(t, goexitCloseSubscriber{}, "topic-goexit", budget)
	elapsed := time.Since(start)

	assertTrue(t, err != nil, "expected sentinel error from defer recover path")
	if elapsed >= budget {
		t.Fatalf("Goexit defense should surface error before budget elapses; took %s (budget=%s)", elapsed, budget)
	}
	assertTrue(t, strings.Contains(err.Error(), "Goexit"),
		"error must explain Goexit exit, got: "+err.Error())
}

func TestCloseWithBudget_PassesErrorThroughOnHappyPath(t *testing.T) {
	t.Parallel()
	wantErr := context.DeadlineExceeded // any sentinel error
	err := closeWithBudget(t, fastCloseSubscriber{err: wantErr}, "topic-ok", time.Second)
	assertSameErrorIdentity(t, err, wantErr, "expected pass-through of subscriber Close error")
}

func TestAwaitWithBudget_TagsErrorWithLabelOnTimeout(t *testing.T) {
	t.Parallel()
	const label = "my-join-site"
	never := make(chan struct{})

	start := time.Now()
	err := awaitWithBudget(label, never, testBudgetAwait)
	elapsed := time.Since(start)

	assertTrue(t, err != nil, "expected timeout error")
	if elapsed < testBudgetAwait {
		t.Fatalf("awaitWithBudget returned before budget elapsed: %s < %s", elapsed, testBudgetAwait)
	}
	if elapsed >= budgetMultiplier5*testBudgetAwait {
		t.Fatalf("awaitWithBudget should return shortly after budget: %s (budget=%s)", elapsed, testBudgetAwait)
	}
	assertTrue(t, strings.Contains(err.Error(), label), "error must include label, got: "+err.Error())
}

func TestAwaitWithBudget_ReturnsNilWhenChanClosesInTime(t *testing.T) {
	t.Parallel()
	ch := make(chan struct{})
	close(ch)
	assertNoError(t, awaitWithBudget("happy", ch, time.Second))
}

// TestCloseWithBudget_ConcurrentSafety: ensures the sentinel + select pattern
// remains race-clean under -race when several budgeted closes run together.
func TestCloseWithBudget_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	const goroutines = 8
	var done atomic.Int32
	for range goroutines {
		go func() {
			_ = closeWithBudget(t, hangingCloseSubscriber{}, "topic-concurrent", testtime.MediumPoll)
			done.Add(1)
		}()
	}
	require.Eventually(t, func() bool {
		return done.Load() >= goroutines
	}, testtime.D5s, testtime.D10ms, "all %d concurrent closeWithBudget calls must return", goroutines)
}

// TestCloseWithBudget_RecoversClosePanic verifies that a panic inside
// Subscriber.Close is recovered by closeWithBudget and surfaced as a
// non-nil error containing the panic value. The process must NOT crash —
// the outer test binary must remain alive (the test would itself crash if
// recover() were absent, proving the fix is load-bearing).
//
// This test validates case (b) in the closeWithBudget goroutine flow:
//   - sub.Close panics → recover() in deferred sentinel catches the value
//   - errCh receives fmt.Errorf("Close panicked: %v", r)
//   - caller receives a non-nil error; process continues
func TestCloseWithBudget_RecoversClosePanic(t *testing.T) {
	t.Parallel()

	err := closeWithBudget(t, panicCloseSubscriber{}, "topic-panic", time.Second)

	assertTrue(t, err != nil, "expected non-nil error when Close panics")
	assertTrue(t, strings.Contains(err.Error(), "panicked"),
		"error must indicate Close panicked, got: "+err.Error())
	assertTrue(t, strings.Contains(err.Error(), "boom"),
		"error must contain the panic value, got: "+err.Error())
}
