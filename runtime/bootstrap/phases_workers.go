package bootstrap

// phases_workers.go — background worker group startup (phase8).
//
// Covers:
//   - phase8StartWorkers: starts the WorkerGroup on runCtx and registers
//     LIFO teardown; no-op when no workers are registered.
//
// ref: uber-go/fx app.go — background goroutine coordination with lifecycle:
// workers use runCtx (independent of external ctx) so lifecycle is owned by
// phase10 teardown, not by the caller cancelling the external context.

import (
	"context"

	"github.com/ghbvf/gocell/runtime/worker"
)

// phase8StartWorkers starts the WorkerGroup using the caller-supplied runCtx
// (independent of external ctx). The workerCancel is only called inside the
// teardown closure so that worker.Stop is the trigger for cancellation during
// phase10.
//
// Key invariant: workerCtx derives from runCtx, NOT from the external ctx.
// ref: uber-go/fx run vs stop ctx separation.
func (b *Bootstrap) phase8StartWorkers(runCtx context.Context, s *phaseState) {
	wg := worker.NewWorkerGroup()
	for _, w := range b.workers {
		wg.Add(w)
	}

	// workerCtx derives from runCtx so external ctx cancel does NOT immediately
	// stop workers. Workers stop only when phase10 calls their teardown.
	workerCtx, workerCancel := context.WithCancel(runCtx)

	if len(b.workers) == 0 {
		workerCancel() // no workers; release immediately
		return
	}

	workerErrCh := make(chan error, 1)
	go func() {
		workerErrCh <- wg.Start(workerCtx)
		close(workerErrCh)
	}()

	s.workerErrCh = workerErrCh
	s.addTeardown(func(c context.Context) error {
		workerCancel()
		stopErr := wg.Stop(c)
		// Wait for the wg.Start goroutine to finish so that all worker goroutines
		// have fully exited before Run() returns.
		//
		// In the ctx-cancel path, phase9 never reads from workerErrCh, so the
		// goroutine is still blocked on wg.Wait(). We drain it here to prevent
		// goroutine leaks and races on state set inside worker goroutines (e.g.,
		// the workerCtxCancelledAt atomic in tests).
		//
		// In the worker-error path, phase9 already drained the error; the channel
		// is closed and this select returns immediately.
		select {
		case <-workerErrCh:
		case <-c.Done():
		}
		return stopErr
	})
}
