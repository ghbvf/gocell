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
// archtest PROBENAME-SEALED-FUNNEL-01, not at runtime — see Probe.Name godoc for
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
	a.probes[name] = healthz.WrapCtxSafe(p, a.clk)
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
