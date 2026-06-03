package idempotency

import "context"

// RequestState is the typed, wire-stable label value identifying the outcome of
// a single idempotency decision. Its value set is frozen as the metric label
// values of idempotency_requests_total{state} and is enforced by archtest
// IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01 (A1 freezes the value set;
// A2 forbids inline literals / RequestState("x") conversions from reaching the
// label, requiring every value to flow from one of the declared constants
// below). The same string-typed-enum funnel is used by the saga executor
// (executor.TickResult / DriveResult / …) and reconcile (resultLabel).
//
// Cardinality: the value set is a closed, registration-time enumeration (6
// values); combined with the bounded {cell} dimension the worst-case series
// count is tiny. There is no per-request / per-key dimension — keys are
// SHA-256 hashed for logs only and never become metric labels.
type RequestState string

const (
	// StateAcquired is recorded once per fresh claim (ClaimAcquired): the lease
	// was acquired and the business handler runs. It is the denominator of
	// processed mutating requests. It is emitted at claim time, BEFORE the
	// handler completes, so it counts every fresh claim regardless of the
	// handler's terminal outcome (successful record, 4xx/5xx, oversize, or
	// panic) — acquired is not the sum of the other states. StateOversize is a
	// sub-event of an acquired request (an acquired request whose response was
	// too large to record increments both acquired and oversize), so
	// oversize/acquired is the uncacheable-response rate.
	StateAcquired RequestState = "acquired"

	// StateReplayed is recorded when a stored response is replayed (ClaimDone):
	// the idempotency key hit and the cached response was served without
	// re-invoking the handler.
	StateReplayed RequestState = "replayed"

	// StateBusy is recorded when an in-flight lease exists for the key
	// (ClaimBusy → 409 with Retry-After). A sustained busy rate signals a
	// retry storm or a stuck in-flight request.
	StateBusy RequestState = "busy"

	// StateStoreError is recorded when Store.Claim returns a non-fingerprint
	// error (→ 500). It is the Claim-path store failure rate. Record/Release
	// failures (after the response is already served) are intentionally not a
	// state — they are logged at slog.Error and are out of this counter's scope.
	StateStoreError RequestState = "store_error"

	// StateOversize is recorded when an acquired request's response body exceeds
	// the buffer limit and is therefore not recorded for future replay
	// (the response is still served to the client normally). See StateAcquired
	// for the acquired ⊇ oversize relationship.
	StateOversize RequestState = "oversize"

	// StateKeyReused is recorded when the same Idempotency-Key is presented with
	// a different request body (fingerprint mismatch → 409 ErrIdempotencyKeyReused).
	// It is security-relevant: a sustained rate can indicate a client bug or a
	// replay attempt.
	StateKeyReused RequestState = "key_reused"
)

// MetricsObserver is the best-effort sink for per-request idempotency metrics.
// The HTTP idempotency Middleware calls ObserveRequest once per idempotency
// state event with the corresponding RequestState. Most requests emit a single
// event; an acquired request whose response is oversize emits two — StateAcquired
// at claim time, then StateOversize when the body overflows (see RequestState for
// the acquired ⊇ oversize relationship). The other five states are mutually
// exclusive and emit once each. The
// interface lives in this producer package (not in runtime/observability/metrics)
// so the middleware never imports the metrics package — mirroring the
// executor.Observer placement that lets runtime/observability/metrics.SagaCollector
// import the producer without an import cycle.
//
// Implementations MUST be non-blocking and tolerate canceled contexts: they run
// on the request hot path and must never affect idempotency correctness. The
// canonical production implementation is
// runtime/observability/metrics.IdempotencyCollector, which records
// idempotency_requests_total{cell,state} and derives the {cell} label from the
// request context. ObserveRequest deliberately takes no cellID argument: the
// owner cell is read from ctx by the collector (kernel/ctxkeys.CellID, set by
// the router root CellAttribution middleware) so the {cell} sentinel single
// source stays in the metrics package and the middleware stays metrics-agnostic.
type MetricsObserver interface {
	ObserveRequest(ctx context.Context, state RequestState)
}
