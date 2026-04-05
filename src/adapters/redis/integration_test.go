//go:build integration

package redis

import (
	"testing"
)

// TestIntegration_ClientPingPong connects to a real Redis instance and
// verifies the PING/PONG handshake.
func TestIntegration_ClientPingPong(t *testing.T) {
	t.Skip("stub: requires Redis (docker compose up)")
}

// TestIntegration_CacheSetGetDelete exercises the Cache adapter with
// real Redis SET / GET / DEL round-trips.
func TestIntegration_CacheSetGetDelete(t *testing.T) {
	t.Skip("stub: requires Redis (docker compose up)")
}

// TestIntegration_DistLockContention acquires a distributed lock from
// two goroutines and asserts mutual exclusion.
func TestIntegration_DistLockContention(t *testing.T) {
	t.Skip("stub: requires Redis (docker compose up)")
}

// TestIntegration_IdempotencyKeyExpiry sets an idempotency key, waits
// for TTL expiry, and verifies the key is gone.
func TestIntegration_IdempotencyKeyExpiry(t *testing.T) {
	t.Skip("stub: requires Redis (docker compose up)")
}
