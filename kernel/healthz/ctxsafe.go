package healthz

// WrapCtxSafe and its companion types implement ctx-racing semantics for Probe
// execution. The implementation is sunk here from runtime/observability/healthz
// so that ctxSafeProbe can implement the sealed isHealthzProbe() marker, which
// is unexported and therefore only expressible within this package.
//
// Package-external code obtains ctx-safe behavior via WrapCtxSafe(p, clk);
// ctxSafeProbe itself is unexported — its Probe identity is exposed only
// through the Probe interface.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/redaction"
)

const (
	// probeLogKey is the slog field key used to record the probe name in all
	// ctxSafeProbe diagnostic log messages.
	probeLogKey = "probe"
)

// probeOutcome carries the return value of the inner Check call so that
// ctxSafeProbe and its late-result watcher share a typed channel element.
type probeOutcome struct {
	err    error
	panicV any
}

// WrapCtxSafe wraps a [Probe] so that its Check method returns as soon as ctx
// is canceled, regardless of whether the underlying function cooperates with
// ctx.Done. This preserves the PR-A35 guarantee from runtime/http/health.
//
// Semantics:
//   - If the inner Check returns before ctx.Done, its return value is used.
//   - If ctx is canceled first, the wrapper returns ctx.Err() immediately.
//     The inner goroutine continues running; its eventual return (or panic) is
//     consumed by a background watcher that logs surprising outcomes.
//   - For realistic I/O-bound probes (DB ping, HTTP call) the inner goroutine
//     terminates at the next I/O boundary. A pathological probe that ignores
//     ctx may leak its goroutine, but the outer contract is structurally held.
//
// The returned value implements [Probe] (including the sealed isHealthzProbe()
// marker) — ctxSafeProbe is the only non-funcProbe implementor permitted by
// the type system.
func WrapCtxSafe(p Probe, clk clock.Clock) Probe {
	return &ctxSafeProbe{inner: p, clk: clk}
}

// ctxSafeProbe implements [Probe] with ctx-racing semantics.
// It is unexported; callers use [WrapCtxSafe] to obtain an instance.
type ctxSafeProbe struct {
	inner Probe
	clk   clock.Clock
}

func (w *ctxSafeProbe) Name() ProbeName { return w.inner.Name() }

func (w *ctxSafeProbe) Check(ctx context.Context) error {
	done := make(chan probeOutcome, 1)
	start := w.clk.Now()
	go func() {
		var out probeOutcome
		defer func() {
			if r := recover(); r != nil {
				out.panicV = r
			}
			done <- out
		}()
		out.err = w.inner.Check(ctx)
	}()
	select {
	case <-ctx.Done():
		// Background watcher: observes the eventual inner outcome so panic
		// values are not silently dropped and operators can grep slog for
		// probes that take a long time to honor cancellation.
		cancelAt := w.clk.Now()
		go watchLateOutcome(w.inner.Name().String(), ctx.Err(), start, cancelAt, done, w.clk)
		return ctx.Err()
	case o := <-done:
		if o.panicV != nil {
			slog.Warn("healthz: probe panicked",
				slog.String(probeLogKey, w.inner.Name().String()),
				slog.Any("panic", redaction.RedactAny(o.panicV)),
			)
			return fmt.Errorf("panic: %v", redaction.RedactAny(o.panicV))
		}
		return o.err
	}
}

// isHealthzProbe implements the sealed marker method — only types within this
// package can implement Probe.
func (*ctxSafeProbe) isHealthzProbe() {}

// watchLateOutcome runs in its own goroutine after the outer Check returned
// ctx.Err(). It observes the inner goroutine's eventual result and logs
// cancel_lag so operators can identify uncooperative probes.
func watchLateOutcome(name string, ctxErr error, start, cancelAt time.Time, done <-chan probeOutcome, clk clock.Clock) {
	o := <-done
	cancelLag := clk.Since(cancelAt)
	probeTotal := clk.Since(start)
	switch {
	case o.panicV != nil:
		slog.Warn("healthz: probe panicked after ctx cancellation; result discarded",
			slog.String(probeLogKey, name),
			slog.Any("panic", redaction.RedactAny(o.panicV)),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	case cancelLag > time.Second:
		slog.Warn("healthz: probe did not honor ctx cancellation promptly",
			slog.String(probeLogKey, name),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	default:
		slog.Debug("healthz: probe canceled, inner fn returned shortly after",
			slog.String(probeLogKey, name),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	}
}
