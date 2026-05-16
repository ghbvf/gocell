//go:build integration

package bootstrap

// TestBootstrapIntegration_OwnerCancel_WorkerExitsBeforeStop verifies the C.2
// owner-ctx contract at bootstrap-layer depth.
//
// Three assertions:
//
//  1. Worker goroutine exits via ownerCtx.Done(): a goroutine spawned inside
//     OnStart that blocks on <-ctx.Done() exits when bootstrap teardown cancels
//     ownerCtx. goleak.VerifyNone confirms no goroutine survives.
//
//  2. LIFO order — ownerCancel before lifecycle.Stop: OnStop observes
//     ownerCtx.Err() != nil, proving that ownerCancel() ran before lifecycle.Stop
//     triggered OnStop. This mirrors the LIFO teardown registration order in
//     bootstrap.go: lifecycle.Stop registered first (runs second), ownerCancel
//     registered second (runs first).
//
//  3. Equivalent coverage for SweeperLifecycle: SweeperLifecycle.Start receives
//     the bootstrap-issued ownerCtx (C.2 contract). A dedicated sub-case wires
//     a real SweeperLifecycle (with a fake SweepTicker) through WithLifecycle into
//     bootstrap and verifies goroutine-exit via goleak after cancel. The unit-level
//     runtime/command.TestSweeperLifecycle_OwnerCancel_ExitsWithoutOnStop already
//     pins the per-component contract; this bootstrap-layer sub-case confirms the
//     end-to-end wire (ownerCtx propagation through lifecycle.Start → SweeperLifecycle.Start).
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-B
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go — internalCtx cancel before Stop.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/command"
)

// fakeSweepTicker is a minimal SweepTicker that records ticks without doing
// any real work. Safe for bootstrap integration tests where we do not want
// the sweeper to perform actual command processing.
type fakeSweepTicker struct {
	mu    sync.Mutex
	ticks int
}

func (f *fakeSweepTicker) SweepTick(_ context.Context, _ time.Time) error {
	f.mu.Lock()
	f.ticks++
	f.mu.Unlock()
	return nil
}

// TestBootstrapIntegration_OwnerCancel_WorkerExitsBeforeStop pins the C.2
// owner-ctx and LIFO teardown order at bootstrap integration depth.
func TestBootstrapIntegration_OwnerCancel_WorkerExitsBeforeStop(t *testing.T) {
	// Assertion 1 + 2: generic hook receives ownerCtx; worker exits via
	// ownerCtx.Done(); OnStop observes ownerCtx already cancelled (LIFO proof).
	t.Run("generic_hook_lifo_order_and_goroutine_exit", func(t *testing.T) {
		// IgnoreCurrent captures the set of goroutines alive before this sub-test
		// so that goroutines owned by sibling tests (e.g. hookDispatcher goroutines
		// from other bootstrap integration tests sharing the same binary) do not
		// cause a false-positive leak failure. We care only about goroutines our
		// test spawns (specifically the worker goroutine inside OnStart).
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

		var (
			workerDone        = make(chan struct{}) // closed when worker goroutine exits
			capturedOwnerCtx  context.Context       // ownerCtx captured in OnStart closure
			ownerCtxErrOnStop error                 // ownerCtx.Err() sampled inside OnStop
			mu                sync.Mutex
		)

		ln := newIntegrationListener(t)
		addr := ln.Addr().String()

		b := New(
			WithClock(clock.Real()),
			WithListener(cell.PrimaryListener, addr, []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
			WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newIntegrationListener(t))),
			WithShutdownTimeout(testtime.D3s),
			WithLifecycle(func(lc Lifecycle) {
				_ = lc.Append(Hook{
					Name: "owner-cancel-probe",
					// OnStart receives ownerCtx (C.2 contract). Capture it so that
					// OnStop can observe its state (ownerCtx vs. the stop ctx that
					// OnStop itself receives are different values). Spawn a goroutine
					// that blocks on ctx.Done() — it must exit when ownerCancel fires,
					// before lifecycle.Stop calls OnStop.
					OnStart: func(ctx context.Context) error {
						mu.Lock()
						capturedOwnerCtx = ctx
						mu.Unlock()
						go func() {
							defer close(workerDone)
							<-ctx.Done() // blocks until ownerCtx is cancelled
						}()
						return nil
					},
					// OnStop is called by lifecycle.Stop (second in LIFO, after
					// ownerCancel). By the time OnStop runs, ownerCtx must already
					// be cancelled — this is the observable LIFO proof.
					// Note: ctx here is the lifecycle stop context (StopTimeout-bounded),
					// NOT ownerCtx. We check capturedOwnerCtx.Err() which is the ctx
					// that the worker goroutine was given in OnStart.
					OnStop: func(_ context.Context) error {
						mu.Lock()
						ownerCtx := capturedOwnerCtx
						mu.Unlock()
						if ownerCtx != nil {
							mu.Lock()
							ownerCtxErrOnStop = ownerCtx.Err()
							mu.Unlock()
						}
						return nil
					},
				})
			}),
		)

		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- b.Run(runCtx) }()

		// Wait for HTTP to become healthy (lifecycle.Start completed at this point).
		waitForIntegrationHealthy(t, addr)

		// Trigger graceful shutdown by cancelling the caller ctx passed to Run.
		// bootstrap.Run then calls phase9AwaitShutdownSignal → phase10, which:
		//   stage 3 LIFO teardown: ownerCancel() first, lifecycle.Stop() second.
		cancel()

		// Wait for Run to return, with a generous deadline.
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("b.Run did not return after cancel")
		}

		// Assertion 1: worker goroutine exited (goleak deferred above also checks).
		// Wait with a timeout so we don't use a non-blocking select that races
		// with goroutine scheduling: the worker exits via ownerCtx.Done() and
		// closes workerDone concurrently with b.Run returning.
		select {
		case <-workerDone:
			// goroutine exited cleanly — OK
		case <-time.After(testtime.EventuallyDefault):
			t.Error("worker goroutine did not exit after ownerCtx cancellation")
		}

		// Assertion 2: LIFO order — ownerCtx.Err() was non-nil when OnStop ran,
		// meaning ownerCancel() fired before lifecycle.Stop() invoked OnStop.
		mu.Lock()
		capturedErr := ownerCtxErrOnStop
		mu.Unlock()
		assert.Equal(t, context.Canceled, capturedErr,
			"OnStop must observe ownerCtx already cancelled (ownerCancel fires before lifecycle.Stop in LIFO order)")
	})

	// Assertion 3: SweeperLifecycle receives bootstrap-issued ownerCtx via the
	// normal WithLifecycle → lifecycle.Start path. Owner cancel exits the sweeper
	// goroutine; no leak survives.
	//
	// Why generic hook is sufficient as complement: the unit-level test
	// runtime/command.TestSweeperLifecycle_OwnerCancel_ExitsWithoutOnStop already
	// pins the per-component SweeperLifecycle.Start(ownerCtx) → goroutine-exit
	// contract in isolation. This sub-case confirms the end-to-end wire: bootstrap
	// passes its ownerCtx (not a separate background ctx) through lifecycle.Start
	// into SweeperLifecycle.Start. The generic hook sub-case above pins the LIFO
	// teardown order (ownerCancel before lifecycle.Stop) which applies equally to
	// SweeperLifecycle — no duplicate LIFO assertion is needed here.
	t.Run("sweeper_lifecycle_via_bootstrap_wire_no_goroutine_leak", func(t *testing.T) {
		// IgnoreCurrent captures the goroutine set at sub-test entry to avoid
		// flagging sibling-test background goroutines (hookDispatcher, etc.).
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

		ticker := &fakeSweepTicker{}
		// Use a large interval so SweepTick is never called during the test;
		// we only care about goroutine lifecycle, not tick behaviour.
		sl := command.NewSweeperLifecycle("owner-cancel-sweeper", ticker, testtime.D1h, clock.Real())

		ln := newIntegrationListener(t)
		addr := ln.Addr().String()

		b := New(
			WithClock(clock.Real()),
			WithListener(cell.PrimaryListener, addr, []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(ln)),
			WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newIntegrationListener(t))),
			WithShutdownTimeout(testtime.D3s),
			WithLifecycle(func(lc Lifecycle) {
				hook := sl.Hook()
				_ = lc.Append(Hook{
					Name:    hook.Name,
					OnStart: hook.OnStart,
					OnStop:  hook.OnStop,
				})
			}),
		)

		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- b.Run(runCtx) }()

		waitForIntegrationHealthy(t, addr)
		cancel()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("b.Run did not return after cancel (sweeper sub-case)")
		}
		// goleak (deferred above) asserts the sweeper goroutine exited cleanly.
	})
}
