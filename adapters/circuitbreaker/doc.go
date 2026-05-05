// Package circuitbreaker provides an in-process three-state circuit breaker
// (closed/half-open/open) that implements the runtime/http/middleware.Allower
// interface.
//
// The state machine follows the generation+expiry model used by sony/gobreaker
// (model reference, not a runtime dependency — the implementation is
// self-contained). Local State and Counts types prevent any third-party
// breaker types from leaking into caller code (CB-ENCAP-01).
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// (D6 PROD-CLOCK-INJECTION-01) — clock injection invariant.
package circuitbreaker
