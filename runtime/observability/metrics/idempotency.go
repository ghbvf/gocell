package metrics

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// IdempotencyCollector records idempotency_requests_total{cell,state} — one
// increment per terminal HTTP idempotency decision made by the idempotency
// middleware. It implements idempotency.MetricsObserver.
//
// Label semantics:
//
//   - {cell}: coarse owner dimension read from ctx via kernel/ctxkeys.CellID
//     (written by the router root CellAttribution middleware). Falls back to
//     RuntimeCellSentinel ("_runtime") for framework paths or requests that do
//     not match any registered RouteGroup. The resolution is identical to the
//     one used by the HTTP metrics middleware for http_requests_total{cell}.
//
//   - {state}: terminal idempotency decision, one of the six
//     idempotency.RequestState constants. The value set is frozen by archtest
//     IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 (A1 freezes the
//     constant set; A2 forbids inline literals or RequestState("x") conversions
//     from reaching the label, requiring every value to flow from one of the
//     declared constants). The string-typed-enum funnel mirrors
//     executor.TickResult / DriveResult / LeaderSkipReason and reconcile
//     resultLabel.
//
// State relationship: StateAcquired ⊇ StateOversize. An acquired request whose
// response body exceeds the buffer limit emits both StateAcquired and
// StateOversize; the ratio oversize/acquired is the uncacheable-response rate.
//
// Cardinality: 6 state values × bounded cell set = tiny worst-case series
// count, well within the metrics provider cap of 2000. No per-request or
// per-key dimension is exposed (keys are SHA-256 hashed for logs only).
//
// Caller contract: to disable metric emission, pass nil to the middleware's
// idempotency.WithMetrics option — it silently skips a nil/typed-nil observer,
// so the middleware runs unmetered. Never pass a nil *IdempotencyCollector to
// ObserveRequest directly — it will dereference a nil CounterVec and panic.
type IdempotencyCollector struct {
	requests kernelmetrics.CounterVec // idempotency_requests_total{cell,state}
}

// compile-time interface check.
var _ idemhttp.MetricsObserver = (*IdempotencyCollector)(nil)

// NewIdempotencyCollector registers idempotency_requests_total on p.
//
// Unlike NewSagaCollector there is no constructor cellID parameter — the HTTP
// idempotency middleware is shared across cells on a listener, so cell is a
// per-record label read from ctx (the same approach used by
// NewProviderCollector and OutboxRejectCollector for http_requests_total).
//
// Failure modes:
//   - p == nil → errcode.KindInvalid + ErrObservabilityConfigInvalid (no fallback)
//   - CounterVec registration error → wrapped with metric name
func NewIdempotencyCollector(p kernelmetrics.Provider) (*IdempotencyCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: IdempotencyCollector Provider is required")
	}
	cv, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name: "idempotency_requests_total",
		Help: "Total HTTP idempotency decisions, labeled by terminal state " +
			"(acquired = fresh claim processed; replayed = cached response served; " +
			"busy = in-flight lease 409; store_error = Claim-path failure 500 " +
			"(Record/Release failures are logged only, not counted here); " +
			"oversize = response too large to record; " +
			"key_reused = same key, different body 409). " +
			"acquired counts every fresh claim; oversize is a sub-event of acquired. " +
			"cell is the owning RouteGroup cell or _runtime for framework paths.",
		LabelNames: []string{"cell", "state"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register idempotency_requests_total: %w", err)
	}
	return &IdempotencyCollector{requests: cv}, nil
}

// ObserveRequest implements idempotency.MetricsObserver. It increments
// idempotency_requests_total{cell,state} once for each terminal idempotency
// decision.
//
// cellID is read from ctx via kernel/ctxkeys.CellID (set by the router root
// CellAttribution middleware), falling back to RuntimeCellSentinel for
// framework-owned paths — the same resolution NewProviderCollector uses for
// http_requests_total{cell}.
//
// The method is non-blocking and tolerates canceled contexts as required by
// MetricsObserver: CounterVec.Inc runs synchronously and never blocks on I/O.
func (c *IdempotencyCollector) ObserveRequest(ctx context.Context, state idemhttp.RequestState) {
	cellID := RuntimeCellSentinel
	if v, ok := ctxkeys.CellIDFrom(ctx); ok && v != "" {
		cellID = v
	}
	c.requests.With(kernelmetrics.Labels{
		"cell":  cellID,
		"state": string(state),
	}).Inc(ctx)
}
