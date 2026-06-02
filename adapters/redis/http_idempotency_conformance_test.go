//go:build integration

package redis

import (
	"testing"
	"time"

	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
	"github.com/ghbvf/gocell/runtime/http/idempotency/idempotencytest"
)

// redisExpiryMargin is a conservative buffer added to sleep durations so that
// Redis server-side PX expiry fires before the next Claim call in conformance tests.
// 300ms provides a more reliable margin on slow CI runners.
const redisExpiryMargin = 300 * time.Millisecond

// realSleepAdvancer implements [idempotencytest.TimeAdvancer] for Redis-backed
// stores. Because the Redis store uses server-side PX expiry (not a Go clock),
// the only portable way to advance past a TTL is to sleep in real time.
//
// AdvancePast sleeps for d plus a small margin to allow Redis to register the
// expiry. This makes the TTL-expiry conformance cases slightly slow for
// integration tests; the tradeoff is correctness without fake-clock coupling to
// the Redis adapter.
type realSleepAdvancer struct{}

func (realSleepAdvancer) AdvancePast(d time.Duration) {
	// Add a modest margin so Redis server-side expiry fires before the next
	// Claim call. redisExpiryMargin is conservative enough for CI and local runs.
	time.Sleep(d + redisExpiryMargin) //archtest:allow:test-sleep waiting for real Redis PX TTL expiry in integration conformance
}

// TestIntegration_HTTPIdempotencyStore_Conformance runs the shared
// idempotencytest conformance suite against the Redis-backed
// [NewHTTPIdempotencyStore]. It replaces the previous hand-written direct
// integration test and ensures the Redis adapter satisfies the same
// backend-agnostic contract as [idemhttp.MemStore].
func TestIntegration_HTTPIdempotencyStore_Conformance(t *testing.T) {
	factory := func(t *testing.T) (idemhttp.Store, idempotencytest.TimeAdvancer, func()) {
		t.Helper()
		client, cleanup := startRedis(t)
		store, err := NewHTTPIdempotencyStore(client, testNamespace)
		if err != nil {
			cleanup()
			t.Fatalf("NewHTTPIdempotencyStore: %v", err)
		}
		return store, realSleepAdvancer{}, cleanup
	}
	idempotencytest.RunConformanceSuite(t, factory)
}
