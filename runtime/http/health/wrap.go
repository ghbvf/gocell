package health

import (
	"context"
	"fmt"
	"log/slog"
	"time"
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
//     value (or panic) is consumed by a background watcher that logs anything
//     surprising (late panic, multi-second cancel lag) so the runaway is at
//     least observable. For realistic I/O-bound probes (DB ping, HTTP call,
//     Redis ping) the inner goroutine terminates on its own at the next I/O
//     boundary or protocol timeout — bounded-lifetime runaway, not a true leak.
//     A pathological probe that calls select{}/for{} still leaks its
//     goroutine, but the outer contract is structurally preserved so the
//     aggregator is unaffected and the watcher records the event.
//   - Panics inside fn propagate out of the goroutine and are surfaced to the
//     outer call site when the race is won by the probe; runOneProbe's
//     recover fence catches them just as it would for a non-wrapped Checker.
//     When the race is won by ctx, a late panic is logged via the watcher
//     rather than silently discarded.
//
// wrapCtxSafe intentionally carries no time budget — the only "deadline"
// relevant here is whatever the caller puts on ctx. Runtime callers
// (runProbesParallel) derive ctx from h.deadline; test callers pass their own.
//
// ref: golang.org/x/sync/singleflight — similar race-pattern idiom used
// throughout the Go ecosystem to bound caller-visible latency regardless of
// inner work.
// probeOutcome captures an inner-fn return so wrapCtxSafe and the
// watchLateProbeOutcome watcher share a single named type. An anonymous
// struct inside wrapCtxSafe would be incompatible with the watcher's
// typed parameter list.
type probeOutcome struct {
	err    error
	panicV any
}

func wrapCtxSafe(fn Checker) Checker {
	if fn == nil {
		// Callers are expected to pre-validate; if somehow nil arrives here,
		// return a Checker that fails closed. This keeps the aggregator safe.
		return func(_ context.Context) error {
			return fmt.Errorf("health: nil checker")
		}
	}
	return func(ctx context.Context) error {
		done := make(chan probeOutcome, 1)
		start := time.Now()
		go func() {
			var out probeOutcome
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
			// Background watcher: observes the eventual inner-fn outcome so
			// panic values are not silently dropped and operators can grep
			// slog for probes that take a long time to honour cancellation.
			// The watcher exits as soon as `done` receives, so it never
			// leaks independently of the inner goroutine.
			//
			// `cancelAt` is captured *here* (not at probe start) so the
			// cancel_lag the watcher logs is the real time between ctx
			// cancellation and the inner fn returning, not the total wall
			// time spent in the probe — a cooperative probe that honours
			// ctx.Done in the very next instruction must produce a near-zero
			// cancel_lag even when ctx fires 5s after probe start.
			cancelAt := time.Now()
			go watchLateProbeOutcome(ctx.Err(), start, cancelAt, done)
			return ctx.Err()
		case o := <-done:
			if o.panicV != nil {
				return fmt.Errorf("panic: %v", o.panicV)
			}
			return o.err
		}
	}
}

// watchLateProbeOutcome runs in its own goroutine after the outer Checker
// has already returned ctx.Err(). It records (a) the gap between ctx
// cancellation and the inner fn finally returning (cancel_lag — F12
// observability) and (b) any panic value the inner fn produced, which
// would otherwise be discarded (F8).
//
// Two durations are captured separately so dashboards can distinguish
// "probe ignored ctx" (large cancel_lag) from "probe was simply slow"
// (large probe_total but cancel_lag near zero):
//
//   - cancel_lag = time.Since(cancelAt) — captured at the moment the outer
//     Checker observed ctx.Done. This is the operative signal for "did the
//     probe honour cancellation"; a cooperative probe must yield ~0 here
//     regardless of how late ctx fired.
//   - probe_total = time.Since(start) — total wall time spent in the inner
//     fn from invocation to return, useful only for cross-checking against
//     the deadline value.
//
// Severity:
//
//   - panic            → Warn (dashboards should grep for this)
//   - cancel_lag > 1s  → Warn so investigators can identify non-cooperative
//     probes (outer contract still held; this is a nudge, not an alarm)
//   - cancel_lag ≤ 1s  → Debug so cooperative probes do not spam prod logs
//
// The watcher receives from `done` exactly once and exits; it cannot leak
// beyond the lifetime of the inner goroutine.
func watchLateProbeOutcome(ctxErr error, start, cancelAt time.Time, done <-chan probeOutcome) {
	o := <-done
	cancelLag := time.Since(cancelAt)
	probeTotal := time.Since(start)
	switch {
	case o.panicV != nil:
		slog.Warn("health: probe panicked after ctx cancellation; result discarded",
			slog.Any("panic", o.panicV),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	case cancelLag > time.Second:
		slog.Warn("health: probe did not honour ctx cancellation promptly",
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	default:
		slog.Debug("health: probe cancelled, inner fn returned shortly after",
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	}
}
