// Package idempotency provides idempotency key management for the GoCell
// kernel.
//
// It defines the interface for idempotency stores and the key format
// convention: {prefix}:{group}:{event-id} with configurable TTL (default 24h).
//
// Concrete implementations are provided by adapters (e.g. adapters/redis).
// The ConsumerBase in the event bus layer uses this package to ensure
// at-least-once delivery does not cause duplicate processing.
package idempotency
