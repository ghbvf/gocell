package redis

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/auth"
)

// testNamespace is the canonical namespace for unit tests that don't
// assert namespace-prefix-derivation themselves. Tests that DO assert key
// derivation use this string literal directly so the prefix is visible
// in the captured Redis call.
const testNamespace KeyNamespace = "testns"

func mustNewCacheFromCmdable(t *testing.T, rdb cmdable) *Cache {
	t.Helper()
	c, err := newCacheFromCmdable(rdb, testNamespace)
	if err != nil {
		t.Fatalf("newCacheFromCmdable: %v", err)
	}
	return c
}

func mustNewIdempotencyClaimerFromCmdable(t *testing.T, rdb cmdable) *IdempotencyClaimer {
	t.Helper()
	c, err := newIdempotencyClaimerFromCmdable(rdb, testNamespace)
	if err != nil {
		t.Fatalf("newIdempotencyClaimerFromCmdable: %v", err)
	}
	return c
}

// mustNewNonceStoreFromCmdable uses nonceTestNamespace (defined in
// nonce_test.go) — distinct from testNamespace so any cross-prefix
// regression surfaces as a missed mock.store lookup. TTL is fixed to
// auth.ServiceTokenNonceTTL because that is the only valid value the
// happy-path tests need; tests that exercise TTL validation call
// newNonceStoreFromCmdable directly.
func mustNewNonceStoreFromCmdable(t *testing.T, rdb cmdable) *NonceStore {
	t.Helper()
	s, err := newNonceStoreFromCmdable(rdb, nonceTestNamespace, auth.ServiceTokenNonceTTL)
	if err != nil {
		t.Fatalf("newNonceStoreFromCmdable: %v", err)
	}
	return s
}

func mustNewRedisDriver(t *testing.T, rdb cmdable) *RedisDriver {
	t.Helper()
	d, err := newRedisDriverFromCmdable(rdb, testNamespace)
	if err != nil {
		t.Fatalf("newRedisDriverFromCmdable: %v", err)
	}
	return d
}
