package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Site-specific startup-budget durations (TEST-TIME-LITERAL-01: no inline
// time literals in test bodies).
const (
	// superviseBudgetDisabled is the WithStartupTimeout sentinel that disables
	// the budget timer (caller-ctx-only abort path).
	superviseBudgetDisabled time.Duration = -1
	// superviseBudget is a finite startup budget for the budget-elapsed test.
	superviseBudget = 30 * time.Second
	// superviseBudgetOvershoot advances the fake clock past superviseBudget.
	superviseBudgetOvershoot = 31 * time.Second
)

// superviseFakeLifecycle is a minimal Lifecycle whose Start behavior is
// scripted by mode:
//
//   - default (blockUntilCancel=false): return immediately with immediateErr.
//   - blockUntilCancel=true: signal startedCh, then block until the (owner)
//     ctx is canceled and return ctx.Err() — the realistic P1-1 shape (a
//     well-behaved hook whose OnStart only completes when ownerCtx is
//     canceled by the supervisor).
type superviseFakeLifecycle struct {
	startedCh        chan struct{}
	immediateErr     error
	blockUntilCancel bool
	// ignoreCtxRelease, when non-nil, models the irreducible-residual shape:
	// an OnStart that ignores ctx entirely and only returns when this channel
	// is closed (the test closes it in Cleanup so the abandoned goroutine does
	// not survive the test — but superviseLifecycleStart must have already
	// returned via the detach path BEFORE that, which is what the test pins).
	ignoreCtxRelease chan struct{}
}

func (f *superviseFakeLifecycle) Append(Hook) error { return nil }

func (f *superviseFakeLifecycle) Start(ctx context.Context) error {
	close(f.startedCh)
	if f.ignoreCtxRelease != nil {
		<-f.ignoreCtxRelease // never selects on ctx.Done() — ctx is ignored
		return f.immediateErr
	}
	if !f.blockUntilCancel {
		return f.immediateErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *superviseFakeLifecycle) Stop(context.Context) error { return nil }

func newSuperviseBootstrap(t *testing.T, lc Lifecycle, fc *clockmock.FakeClock, startupTimeout time.Duration) *Bootstrap {
	t.Helper()
	b := &Bootstrap{lifecycle: lc, clock: fc, startupTimeout: startupTimeout}
	b.ownerCtx, b.ownerCancel = context.WithCancel(context.Background())
	t.Cleanup(b.ownerCancel) // ensure ctx released even on the happy path
	return b
}

// TestSuperviseLifecycleStart_HappyPath — Start returns nil promptly.
func TestSuperviseLifecycleStart_HappyPath(t *testing.T) {
	fl := &superviseFakeLifecycle{startedCh: make(chan struct{})}
	b := newSuperviseBootstrap(t, fl, clockmock.New(time.Unix(0, 0)), DefaultStartupTimeout)
	require.NoError(t, b.superviseLifecycleStart(context.Background()))
}

// TestSuperviseLifecycleStart_CallerCtxCancelAbortsWedgedStart — the primary
// P1-1 abort path: a hook whose OnStart only completes on ctx cancel would
// wedge Run() because ownerCtx is background-derived. The supervisor must
// cancel ownerCtx on caller cancel so Start unwinds and Run proceeds.
func TestSuperviseLifecycleStart_CallerCtxCancelAbortsWedgedStart(t *testing.T) {
	fl := &superviseFakeLifecycle{startedCh: make(chan struct{}), blockUntilCancel: true}
	b := newSuperviseBootstrap(t, fl, clockmock.New(time.Unix(0, 0)), superviseBudgetDisabled) // budget disabled — caller ctx only

	callerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.superviseLifecycleStart(callerCtx) }()

	<-fl.startedCh // Start entered and is blocked on its (owner) ctx
	cancel()       // caller aborts startup

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled, "caller cause must be joined")
	case <-time.After(testtime.D1s):
		t.Fatal("superviseLifecycleStart did not return after caller ctx cancel — Run() would be wedged (P1-1)")
	}
}

// TestSuperviseLifecycleStart_StartupBudgetElapsed — caller never cancels and
// OnStart never returns: the startup budget must fire, cancel ownerCtx, and
// return ErrBootstrapStartupTimeout so Run() makes progress.
func TestSuperviseLifecycleStart_StartupBudgetElapsed(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	fl := &superviseFakeLifecycle{startedCh: make(chan struct{}), blockUntilCancel: true}
	b := newSuperviseBootstrap(t, fl, fc, superviseBudget)

	// resultCh carries the superviseLifecycleStart return value exactly once.
	resultCh := make(chan error, 1)
	go func() { resultCh <- b.superviseLifecycleStart(context.Background()) }()

	<-fl.startedCh
	// Let the supervisor arm its budget timer, then blow past it.
	// Advance the fake clock in the Eventually loop until the goroutine exits.
	var got error
	var closed bool
	require.Eventually(t, func() bool {
		fc.Advance(superviseBudgetOvershoot)
		select {
		case err := <-resultCh:
			got = err
			closed = true
			return true
		default:
			return false
		}
	}, testtime.D1s, testtime.D1ms, "budget timer never fired")

	require.True(t, closed, "superviseLifecycleStart goroutine did not return")
	require.Error(t, got)
	assert.ErrorIs(t, got, ErrBootstrapStartupTimeout)
}

// TestSuperviseLifecycleStart_CtxIgnoringHookDetaches pins the residual
// boundary: an OnStart that ignores ctx entirely and never returns CANNOT be
// force-killed in Go (the leaked goroutine is irreducible). The backstop's
// contract is narrower — it guarantees Bootstrap.Run still makes deterministic
// progress: after the budget fires and ownerCancel() does not unblock the
// ctx-ignoring hook, the startupUnwindGraceTimeout elapses, the in-flight
// lifecycle.Start goroutine is ABANDONED, and superviseLifecycleStart returns
// (so Run() rolls back + exits and the orchestrator restarts the process)
// rather than wedging forever on a bare <-startErr (pre-amendment behavior).
func TestSuperviseLifecycleStart_CtxIgnoringHookDetaches(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	release := make(chan struct{})
	// Close release in Cleanup so the deliberately-abandoned Start goroutine
	// exits with the test process instead of leaking — the assertion below
	// proves superviseLifecycleStart already returned via the detach path
	// while this channel was still open (i.e. the hook never observed ctx).
	t.Cleanup(func() { close(release) })
	fl := &superviseFakeLifecycle{startedCh: make(chan struct{}), ignoreCtxRelease: release}
	b := newSuperviseBootstrap(t, fl, fc, superviseBudget)

	resultCh := make(chan error, 1)
	go func() { resultCh <- b.superviseLifecycleStart(context.Background()) }()

	<-fl.startedCh
	// Advance past the startup budget AND the post-cancel unwind grace; the
	// ctx-ignoring hook stays blocked on `release` the whole time, so the only
	// way superviseLifecycleStart returns is the detach (grace-timer) branch.
	var got error
	var closed bool
	require.Eventually(t, func() bool {
		fc.Advance(superviseBudgetOvershoot + startupUnwindGraceTimeout)
		select {
		case err := <-resultCh:
			got, closed = err, true
			return true
		default:
			return false
		}
	}, testtime.D1s, testtime.D1ms, "supervisor never detached the ctx-ignoring Start goroutine")

	require.True(t, closed, "superviseLifecycleStart did not return — Run() would wedge on a ctx-ignoring hook")
	require.Error(t, got)
	assert.ErrorIs(t, got, ErrBootstrapStartupTimeout, "budget cause must be joined")
	assert.Contains(t, got.Error(), "abandoned", "error must explain WHY Run is exiting")
	assert.Contains(t, got.Error(), "OnStart ignored ctx")
}
