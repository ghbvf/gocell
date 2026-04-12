// Package circuitbreaker provides a sony/gobreaker adapter that implements
// the runtime/http/middleware.CircuitBreakerPolicy interface.
//
// ref: sony/gobreaker — TwoStepCircuitBreaker with three-state machine
// Adopted: TwoStepCircuitBreaker.Allow/done for the two-step HTTP pattern.
// Deviated: wrapped behind middleware.CircuitBreakerPolicy so runtime/ remains
// decoupled from gobreaker imports.
package circuitbreaker
