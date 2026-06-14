// Package circuitbreaker provides an in-process three-state circuit breaker
// (closed/half-open/open) as a clock-only kernel primitive: it has no
// infrastructure dependency, only kernel/clock for time.
//
// The state machine follows the generation+expiry model used by sony/gobreaker
// (model reference, not a runtime dependency — the implementation is
// self-contained). Local State and Counts types prevent any third-party
// breaker types from leaking into caller code.
//
// Two consumers share this one machine:
//   - kernel/webhook gates per-endpoint outbound delivery on a Breaker so an
//     unhealthy receiver is fast-failed (Requeue) instead of repeatedly POSTed.
//   - runtime/http/middleware.CircuitBreaker accepts a Breaker as its Allower
//     (the Allow/done two-step protocol + RetryAfter structurally match the
//     middleware.Allower / CircuitBreakerRetryAfter interfaces; kernel does not
//     import runtime, so the match is asserted in the middleware test).
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// (D6 PROD-CLOCK-INJECTION-01) — clock injection invariant.
package circuitbreaker
