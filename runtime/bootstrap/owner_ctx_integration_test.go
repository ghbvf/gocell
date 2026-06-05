//go:build integration

package bootstrap

// TestBootstrapIntegration_OwnerCancel_WorkerExitsBeforeStop verifies the C.2
// owner-ctx contract at bootstrap-layer depth.
//
// Two assertions:
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
// The generic hook sub-case covers any real lifecycle component wired through
// WithLifecycle (including the device-command reconcile.Loop, whose own
// goroutine lifecycle is pinned by kernel/reconcile/loop_test.go); no
// component-specific bootstrap sub-case is needed.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-B
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go — internalCtx cancel before Stop.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

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
		healthLn := newIntegrationListener(t)

		b := New(
			clock.Real(),
			WithListener(cell.PrimaryListener, addr, []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(ln)),
			WithListener(cell.InternalListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(newIntegrationListener(t))),
			WithListener(cell.HealthListener, healthLn.Addr().String(), []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(healthLn)),
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
		waitForIntegrationHealthy(t, healthLn.Addr().String())

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
}
