package healthz

import (
	"context"
	"time"
)

// Aggregator is the central probe registry and read-side evaluator.
//
// Write side (Register / Deregister) is invoked at cell Init / Stop
// boundaries to attach probes to the framework lifecycle. Read side
// (Evaluate) is invoked by HTTP transports and persistence adapters when
// they need a fresh Snapshot.
//
// There is intentionally no Latest() method — the in-memory aggregator
// always evaluates synchronously on demand, and the optional persistence
// adapters (postgres / otel) are deferred per the M1 issue scope. Adding
// Latest() back in is straightforward when those adapters land.
//
// Implementations must be concurrent-safe: Register / Deregister may run
// concurrently with Evaluate. The HTTP transport may issue many Evaluate
// calls in parallel from the singleflight layer; aggregator implementations
// are not expected to dedupe — that is the transport's responsibility.
//
// ref: spring-projects/spring-boot CompositeHealthContributor — registry
// with named contributors; GoCell uses a flat list (no nested composites).
// ref: heptiolabs/healthcheck Handler.AddReadinessCheck — flat list,
// AND aggregation.
type Aggregator interface {
	// Register adds a probe. Returns ErrDuplicateProbe if a probe with the
	// same Name() is already registered.
	Register(p Probe) error

	// Deregister removes the probe with the given name. No-op if the name
	// is not currently registered.
	Deregister(name string)

	// Evaluate runs all registered probes synchronously and returns a fresh
	// Snapshot. Each probe is invoked with a context derived from ctx; the
	// deadline / cancellation semantics are implementation-defined (the
	// default in-memory aggregator uses a per-probe deadline configured at
	// construction time).
	Evaluate(ctx context.Context) Snapshot
}

// Snapshot is the immutable result of one aggregation pass.
//
// Overall is the worst-case Status across all probes (per WorseStatus). An
// empty Probes slice yields Overall = StatusUp.
//
// Probes order is implementation-defined; the in-memory aggregator returns
// probes sorted by Name for stable verbose output. Wire transports must
// not assume insertion order.
type Snapshot struct {
	Overall Status
	Probes  []ProbeResult
}

// ProbeResult captures one probe's outcome inside a Snapshot.
//
// Err is nil iff Status == StatusUp. Wire transports MUST NOT serialize
// Err verbatim — the runtime/http/health four-channel redaction model
// requires routing the error text through pkg/redaction before it can
// reach a slog backend, and Err must never appear on the public wire body
// (per HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01).
//
// Latency measures the wall-clock duration of Check(); the aggregator
// records it under whatever clock its constructor was given.
type ProbeResult struct {
	Name    string
	Status  Status
	Err     error
	Latency time.Duration
}
