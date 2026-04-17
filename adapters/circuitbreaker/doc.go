// Package circuitbreaker provides a sony/gobreaker adapter that implements
// the runtime/http/middleware.Allower interface.
//
// ref: sony/gobreaker — TwoStepCircuitBreaker with three-state machine
// Adopted: TwoStepCircuitBreaker.Allow/done for the two-step HTTP pattern.
// Deviated: wrapped behind middleware.Allower so runtime/ remains
// decoupled from gobreaker imports. Local State and Counts types prevent
// gobreaker types from leaking into caller code (CB-ENCAP-01).
package circuitbreaker
