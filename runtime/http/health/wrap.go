package health

import (
	"context"
	"fmt"
)

// wrapCtxSafe turns an arbitrary Checker into a race-pattern Checker whose
// outer call returns as soon as the caller's ctx is cancelled, regardless of
// whether the inner function cooperates with ctx.Done. This is the structural
// replacement for the pre-PR-A35 "uncooperative probe leaks past the
// aggregator" trade-off.
//
// Semantics:
//   - If inner fn returns before ctx.Done, its return value is propagated.
//   - If ctx is cancelled first, the outer Checker returns ctx.Err() immediately.
//     The inner goroutine continues running to completion; its eventual return
//     value is discarded. For realistic I/O-bound probes (DB ping, HTTP call,
//     Redis ping) the inner goroutine terminates on its own at the next I/O
//     boundary or protocol timeout — this is a bounded-lifetime runaway rather
//     than a true leak. A pathological probe that calls select{}/for{} without
//     any exit path still leaks its goroutine, but since the outer contract is
//     structurally preserved the aggregator is no longer affected.
//   - Panics inside fn propagate out of the goroutine and are surfaced to the
//     outer call site; runOneProbe's recover fence catches them just as it
//     would for a non-wrapped Checker.
//
// wrapCtxSafe intentionally carries no time budget — the only "deadline"
// relevant here is whatever the caller puts on ctx. Runtime callers
// (runProbesParallel) derive ctx from h.deadline; test callers pass their own.
//
// ref: golang.org/x/sync/singleflight — similar race-pattern idiom used
// throughout the Go ecosystem to bound caller-visible latency regardless of
// inner work.
func wrapCtxSafe(fn Checker) Checker {
	if fn == nil {
		// Callers are expected to pre-validate; if somehow nil arrives here,
		// return a Checker that fails closed. This keeps the aggregator safe.
		return func(_ context.Context) error {
			return fmt.Errorf("health: nil checker")
		}
	}
	return func(ctx context.Context) error {
		type outcome struct {
			err    error
			panicV any
		}
		done := make(chan outcome, 1)
		go func() {
			var out outcome
			defer func() {
				if r := recover(); r != nil {
					out.panicV = r
				}
				done <- out
			}()
			out.err = fn(ctx)
		}()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case o := <-done:
			if o.panicV != nil {
				panic(o.panicV)
			}
			return o.err
		}
	}
}
