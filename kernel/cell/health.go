package cell

import "errors"

// ErrDegraded is the canonical sentinel returned by cell.HealthContributor
// checkers to signal "operational but degraded" — the cell is still serving
// traffic, but a non-critical capability is impaired (e.g., outbox fail-open
// drop rate exceeded threshold). The /readyz aggregator detects ErrDegraded
// via errors.Is and maps it to HTTP 200 + body status="degraded" rather than
// 503, so K8s readinessProbe does not evict the pod for soft-failure signals.
//
// Operators should pair degraded responses with Prometheus alerts on the
// underlying metric (e.g., gocell_outbox_emit_failopen_dropped_total) for
// the actionable signal; ErrDegraded is the on-the-wire indicator that the
// pod knows it is in a degraded state.
//
// ref: envoyproxy/envoy admin /ready — DEGRADED returns 200, distinguishing
// "soft failure, do not evict" from "hard failure, drain traffic".
// ref: HealthStatus.Status type tag in kernel/cell/types.go:63
// ("healthy" | "degraded" | "unhealthy") — degraded is already a first-class
// state on the per-cell HealthStatus; this sentinel extends it to the
// per-checker plane.
var ErrDegraded = errors.New("cell: degraded")
