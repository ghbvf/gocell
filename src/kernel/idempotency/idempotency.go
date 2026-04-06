// Package idempotency defines the consumer-side idempotency interface.
// Implementations live in adapters/ (e.g., adapters/redis).
package idempotency

import (
	"context"
	"time"
)

// DefaultTTL is the standard idempotency key TTL per the EventBus specification.
const DefaultTTL = 24 * time.Hour

// Checker provides idempotency checking for event consumers.
// The standard TTL is 24 hours per the EventBus specification.
type Checker interface {
	// IsProcessed returns true if the given key has already been processed.
	IsProcessed(ctx context.Context, key string) (bool, error)

	// MarkProcessed marks the key as processed with the given TTL.
	MarkProcessed(ctx context.Context, key string, ttl time.Duration) error

	// TryProcess atomically checks whether key has been processed and marks it if not.
	// Returns true if the caller should process (key was not previously seen).
	// Returns false if already processed (another consumer got there first).
	// This eliminates the TOCTOU race between separate IsProcessed + MarkProcessed calls.
	TryProcess(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// Release removes the idempotency key so that a redelivered message can be
	// processed again. Must be called on requeue/shutdown paths where TryProcess
	// already claimed the key but business logic did not complete.
	Release(ctx context.Context, key string) error
}
