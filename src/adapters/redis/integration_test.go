//go:build integration

// Package redis provides the Redis adapter for GoCell.
// Integration tests require a running Redis instance.
package redis

import "testing"

// TestIntegration_PingConnection verifies basic connectivity to Redis.
func TestIntegration_PingConnection(t *testing.T) {
	t.Skip("stub: requires running Redis instance")
}

// TestIntegration_SetGetDelete verifies basic key-value operations.
func TestIntegration_SetGetDelete(t *testing.T) {
	t.Skip("stub: requires running Redis instance")
}

// TestIntegration_TTLExpiry verifies that keys with TTL expire as expected.
func TestIntegration_TTLExpiry(t *testing.T) {
	t.Skip("stub: requires running Redis instance")
}

// TestIntegration_IdempotencyKey verifies idempotency key set-if-absent semantics.
func TestIntegration_IdempotencyKey(t *testing.T) {
	t.Skip("stub: requires running Redis instance")
}

// TestIntegration_Close verifies graceful shutdown releases the connection pool.
func TestIntegration_Close(t *testing.T) {
	t.Skip("stub: requires running Redis instance")
}
