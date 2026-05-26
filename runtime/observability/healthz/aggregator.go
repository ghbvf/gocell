package healthz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/redaction"
)

const (
	// defaultDeadline is the per-probe execution budget. Matches the Kubernetes
	// readiness probe default periodSeconds=10 / timeoutSeconds=5 convention.
	defaultDeadline = 5 * time.Second
)

// Option configures a [aggregator] at construction time.
type Option func(*aggregator)

// WithDeadline sets the per-probe execution deadline. The zero or negative
// value is rejected by NewAggregator (panic via MustHavePositiveInterval).
func WithDeadline(d time.Duration) Option {
	return func(a *aggregator) {
		a.deadline = d
	}
}

// aggregator is the unexported concrete implementation of [healthz.Aggregator].
// All exported access is through the interface value returned by NewAggregator.
type aggregator struct {
	mu       sync.RWMutex
	probes   map[healthz.ProbeName]healthz.Probe // name → ctx-safe wrapped probe
	deadline time.Duration
	clk      clock.Clock
}

// NewAggregator returns a [healthz.Aggregator] backed by an in-memory probe
// registry. It is the default implementation used by bootstrap.
//
// Construction-time invariants (fail-fast panics):
//   - clock must not be nil (use [clock.Real] in production or [clockmock.New]
//     in tests); enforced by [clock.MustHaveClock].
//   - deadline must be positive; defaults to 5s if [WithDeadline] is not
//     supplied (safe default; no panic for missing option).
//
// Concurrent safety: Register / Deregister / Evaluate are all safe for
// concurrent use. Evaluate never holds the write lock; Register and Deregister
// take the write lock only during map mutation.
func NewAggregator(clk clock.Clock, opts ...Option) healthz.Aggregator {
	a := &aggregator{
		probes:   make(map[healthz.ProbeName]healthz.Probe),
		deadline: defaultDeadline,
		clk:      clk,
	}
	for _, o := range opts {
		o(a)
	}
	clock.MustHaveClock(clk, "runtime/observability/healthz.NewAggregator")
	clock.MustHavePositiveInterval(a.deadline, "runtime/observability/healthz.NewAggregator deadline")
	return a
}

// Register adds p to the registry. Returns [healthz.ErrInvalidProbeName] (via
// errors.Is) when p is nil or its Name() is empty, and [healthz.ErrDuplicateProbe]
// when a probe with the same Name() is already registered. Name shape
// (snake_case + _ready suffix for dependency probes) is enforced statically by
// archtest READYZ-PROBE-NAMING-01, not at runtime — see Probe.Name godoc for
// the single-source-of-truth rationale. The probe's Check function is wrapped
// with a ctx-safe racing wrapper at registration time so that a canceled
// context always terminates the outer call even when the underlying function
// is uncooperative.
func (a *aggregator) Register(p healthz.Probe) error {
	if p == nil {
		return fmt.Errorf("%w: nil probe", healthz.ErrInvalidProbeName)
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("%w: empty probe name", healthz.ErrInvalidProbeName)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.probes[name]; exists {
		return fmt.Errorf("%w: probe %q", healthz.ErrDuplicateProbe, name)
	}
	a.probes[name] = wrapProbeCtxSafe(p, a.clk)
	return nil
}

// Deregister removes the probe with the given name. No-op if the name is not
// currently registered.
func (a *aggregator) Deregister(name healthz.ProbeName) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.probes, name)
}

// Evaluate runs all registered probes concurrently and returns a [healthz.Snapshot].
//
// Each probe runs under a context derived from the provided ctx via
// [context.WithoutCancel] plus the configured per-probe deadline: probe
// execution inherits request-scoped values (e.g. trace IDs, slog attrs) but
// is decoupled from request-level cancellation — a kubelet disconnect (ctx
// cancellation) must not cancel probe execution mid-flight, since the probe
// result should reflect real dependency health, not transport noise. The
// HTTP transport (runtime/http/health.ReadyzHandler) hands the request ctx
// directly; under singleflight the first request's ctx values reach the
// probe — see ReadyzHandler godoc for the first-wins ctx contract.
//
// The returned Snapshot.Probes slice is sorted by Name for stable wire output.
// Snapshot.Overall is the worst-case status across all probes per
// [healthz.WorseStatus]; an empty registry returns StatusUp.
func (a *aggregator) Evaluate(ctx context.Context) healthz.Snapshot {
	// Snapshot the probe map under RLock; release before running probes.
	a.mu.RLock()
	probes := make([]healthz.Probe, 0, len(a.probes))
	for _, p := range a.probes {
		probes = append(probes, p)
	}
	a.mu.RUnlock()

	if len(probes) == 0 {
		return healthz.Snapshot{
			Overall: healthz.StatusUp,
			Probes:  []healthz.ProbeResult{},
		}
	}

	// Probe context: inherits ctx values (trace) via WithoutCancel but is
	// decoupled from ctx cancellation, then bounded by the per-probe deadline.
	// See godoc for the cancellation-isolation rationale.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.deadline)
	defer cancel()

	results := make([]healthz.ProbeResult, len(probes))
	var wg sync.WaitGroup
	wg.Add(len(probes))
	for i, p := range probes {
		i, p := i, p
		go func() {
			defer wg.Done()
			results[i] = a.runOneProbe(probeCtx, p)
		}()
	}
	wg.Wait()

	// Sort by name for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	// Fold to aggregate overall status.
	overall := healthz.StatusUp
	for _, r := range results {
		overall = healthz.WorseStatus(overall, r.Status)
	}

	return healthz.Snapshot{
		Overall: overall,
		Probes:  results,
	}
}

// runOneProbe executes a single probe inside a recover fence. Panics become
// StatusDown with an error message prefixed "panic:". The deadline value is
// included in timeout error strings so verbose consumers can see the exact
// budget without consulting runtime configuration.
//
// Classification rules (matching runtime/http/health.runOneProbe logic):
//   - err == nil                                   → StatusUp
//   - errors.Is(ctx.Err()|err, DeadlineExceeded)   → StatusDown (timeout)
//   - errors.Is(err, outbox.ErrDegraded)             → StatusDegraded (fail-open)
//   - any other non-nil error                      → StatusDown
func (a *aggregator) runOneProbe(ctx context.Context, p healthz.Probe) (pr healthz.ProbeResult) {
	pr.Name = p.Name()
	start := a.clk.Now()
	defer func() {
		pr.Latency = a.clk.Since(start)
		if r := recover(); r != nil {
			slog.Warn("healthz: probe panicked",
				slog.String("probe", pr.Name.String()),
				slog.Any("panic", redaction.RedactAny(r)),
			)
			pr.Status = healthz.StatusDown
			pr.Err = fmt.Errorf("panic: %v", redaction.RedactAny(r))
		}
	}()

	err := p.Check(ctx)
	switch {
	case err == nil:
		pr.Status = healthz.StatusUp
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		pr.Status = healthz.StatusDown
		pr.Err = fmt.Errorf("probe did not return within deadline %s (ctx: %w)", a.deadline, err)
	case errors.Is(err, outbox.ErrDegraded):
		pr.Status = healthz.StatusDegraded
		pr.Err = err
	default:
		pr.Status = healthz.StatusDown
		pr.Err = err
	}
	return pr
}

// probeOutcome carries the return value of the inner Check call so that
// wrapProbeCtxSafe and its late-result watcher share a typed channel element.
type probeOutcome struct {
	err    error
	panicV any
}

// wrapProbeCtxSafe wraps a [healthz.Probe] so that its Check method returns
// as soon as ctx is canceled, regardless of whether the underlying function
// cooperates with ctx.Done. This preserves the PR-A35 guarantee from
// runtime/http/health.wrapCtxSafe.
//
// Semantics:
//   - If the inner Check returns before ctx.Done, its return value is used.
//   - If ctx is canceled first, the wrapper returns ctx.Err() immediately.
//     The inner goroutine continues running; its eventual return (or panic) is
//     consumed by a background watcher that logs surprising outcomes.
//   - For realistic I/O-bound probes (DB ping, HTTP call) the inner goroutine
//     terminates at the next I/O boundary. A pathological probe that ignores
//     ctx may leak its goroutine, but the outer contract is structurally held.
func wrapProbeCtxSafe(p healthz.Probe, clk clock.Clock) healthz.Probe {
	return &ctxSafeProbe{inner: p, clk: clk}
}

// ctxSafeProbe implements [healthz.Probe] with ctx-racing semantics.
type ctxSafeProbe struct {
	inner healthz.Probe
	clk   clock.Clock
}

func (w *ctxSafeProbe) Name() healthz.ProbeName { return w.inner.Name() }

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
				slog.String("probe", w.inner.Name().String()),
				slog.Any("panic", redaction.RedactAny(o.panicV)),
			)
			return fmt.Errorf("panic: %v", redaction.RedactAny(o.panicV))
		}
		return o.err
	}
}

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
			slog.String("probe", name),
			slog.Any("panic", redaction.RedactAny(o.panicV)),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	case cancelLag > time.Second:
		slog.Warn("healthz: probe did not honor ctx cancellation promptly",
			slog.String("probe", name),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	default:
		slog.Debug("healthz: probe canceled, inner fn returned shortly after",
			slog.String("probe", name),
			slog.Any("ctx_err", ctxErr),
			slog.Duration("cancel_lag", cancelLag),
			slog.Duration("probe_total", probeTotal),
		)
	}
}
