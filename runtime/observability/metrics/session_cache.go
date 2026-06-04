package metrics

import (
	"context"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// SessionCacheCollector registers the three session-cache metrics scoped to the
// owner cell supplied at construction. One collector per CachingSessionStore
// (= per-cell; the session cache is always owned by exactly one cell —
// accesscore today). It is the Prometheus sink for the read-through Redis
// session cache (adapters/redis.CachingSessionStore), distinguishing
// cache-hit-rate erosion from partial Redis faults that previously were only
// visible in slog (#794).
//
//   - session_cache_hits_total{cell}: clean cache hits (a well-formed, valid
//     entry was served from Redis without touching the inner store).
//   - session_cache_misses_total{cell}: cache misses (no entry in Redis; the
//     inner store was consulted and the result lazily populated).
//   - session_cache_errors_total{cell}: cache-access errors (Redis GET/SET/DEL
//     failure, corrupt JSON, or a schema-invalid entry). All are fail-safe —
//     they degrade to a miss and never propagate to the caller — but their rate
//     distinguishes "cache cold" from "Redis degraded". A single Get may emit
//     both a miss and an error (e.g. a corrupt entry → evict-and-fall-through):
//     the two series are orthogonal events, not mutually exclusive.
//
// Cardinality: the sole label dimension is `cell`, a registration-time
// enumerated value (the owning cell ID), so total series = number of cells with
// a session cache enabled (1 today).
//
// The cell label is baked at construction (mirrors SagaCollector) rather than
// resolved per-record via metrics.ResolveCellLabel: the session cache is a
// per-cell adapter primitive, not request-scoped framework middleware, so its
// owner is fixed at wiring time.
//
// ref: prometheus/client_golang prometheus/counter.go (CounterVec).
// ref: runtime/observability/metrics/saga.go (per-cell collector pattern).
type SessionCacheCollector struct {
	cellID string

	hits   kernelmetrics.CounterVec // session_cache_hits_total{cell}
	misses kernelmetrics.CounterVec // session_cache_misses_total{cell}
	errors kernelmetrics.CounterVec // session_cache_errors_total{cell}
}

// NewSessionCacheCollector registers the three session-cache counters on the
// given provider. cellID is the owner cell of the CachingSessionStore wiring
// this collector; empty is an error (no fallback to the _runtime sentinel — a
// per-cell cache always has exactly one owner).
//
// Failure modes:
//   - p == nil → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - cellID == "" → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - any CounterVec registration error is wrapped with the metric name and
//     rolls back the counters registered earlier in the sequence (atomic).
func NewSessionCacheCollector(p kernelmetrics.Provider, cellID string) (*SessionCacheCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SessionCacheCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SessionCacheCollector cellID is required")
	}

	var registered []kernelmetrics.Collector
	c := &SessionCacheCollector{cellID: cellID}
	var err error
	if c.hits, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name:       "session_cache_hits_total",
		Help:       "Total session-cache hits (valid entry served from Redis without consulting the inner store).",
		LabelNames: []string{"cell"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.misses, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name:       "session_cache_misses_total",
		Help:       "Total session-cache misses (no Redis entry; inner store consulted and result lazily populated).",
		LabelNames: []string{"cell"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.errors, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "session_cache_errors_total",
		Help: "Total session-cache access errors (Redis GET/SET/DEL failure, corrupt JSON, or schema-invalid entry). " +
			"Fail-safe — every error degrades to a miss — so this rate distinguishes a cold cache from a degraded Redis.",
		LabelNames: []string{"cell"},
	}, &registered); err != nil {
		return nil, err
	}
	return c, nil
}

// RecordHit increments session_cache_hits_total{cell}. Nil-receiver safe so a
// disabled cache (no collector wired) is a no-op.
func (c *SessionCacheCollector) RecordHit(ctx context.Context) {
	if c == nil {
		return
	}
	c.hits.With(kernelmetrics.Labels{"cell": c.cellID}).Inc(ctx)
}

// RecordMiss increments session_cache_misses_total{cell}. Nil-receiver safe.
func (c *SessionCacheCollector) RecordMiss(ctx context.Context) {
	if c == nil {
		return
	}
	c.misses.With(kernelmetrics.Labels{"cell": c.cellID}).Inc(ctx)
}

// RecordError increments session_cache_errors_total{cell}. Nil-receiver safe.
func (c *SessionCacheCollector) RecordError(ctx context.Context) {
	if c == nil {
		return
	}
	c.errors.With(kernelmetrics.Labels{"cell": c.cellID}).Inc(ctx)
}
