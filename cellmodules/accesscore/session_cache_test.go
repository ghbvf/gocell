package accesscore

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/composition"
)

// sessionCacheTestMaxTTL mirrors sessionCacheTTLMax for in-package tests.
const sessionCacheTestMaxTTL = sessionCacheTTLMax

// newTestSessionMemStore returns a minimal in-memory session.Store for tests.
// Protocol is configured to match the real protocol used in buildAccessBaseOpts.
func newTestSessionMemStore(t *testing.T) session.Store {
	t.Helper()
	proto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(t, err)
	store, err := session.NewMemStore(proto, clock.Real())
	require.NoError(t, err)
	return store
}

func TestWrapSessionStoreWithCache_EmptyTTLEnv_ReturnsInnerUnchanged(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "")
	inner := newTestSessionMemStore(t)
	shared := &composition.SharedDeps{}
	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	assert.Same(t, inner, got, "empty TTL env should return inner store unchanged")
}

func TestWrapSessionStoreWithCache_InvalidDuration_SilentDowngrade(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "not-a-duration")
	inner := newTestSessionMemStore(t)
	shared := &composition.SharedDeps{}
	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	assert.Same(t, inner, got, "invalid duration should silently return inner store")
}

func TestWrapSessionStoreWithCache_ZeroTTL_SilentDowngrade(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "0s")
	inner := newTestSessionMemStore(t)
	shared := &composition.SharedDeps{}
	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	assert.Same(t, inner, got, "zero TTL should silently return inner store")
}

func TestWrapSessionStoreWithCache_NegativeTTL_SilentDowngrade(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "-5s")
	inner := newTestSessionMemStore(t)
	shared := &composition.SharedDeps{}
	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	assert.Same(t, inner, got, "negative TTL should silently return inner store")
}

func TestWrapSessionStoreWithCache_ExceedsMaxTTL_ReturnsError(t *testing.T) {
	exceed := sessionCacheTestMaxTTL + time.Second
	t.Setenv(envSessionCacheTTL, exceed.String())
	inner := newTestSessionMemStore(t)
	shared := &composition.SharedDeps{}
	_, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.Error(t, err, "TTL exceeding max should return an error")
	assert.Contains(t, err.Error(), "GOCELL_SESSION_CACHE_TTL")
}

func TestWrapSessionStoreWithCache_ValidTTLNoRedis_SilentDowngrade(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "5s")
	inner := newTestSessionMemStore(t)
	// Redis is nil — should silently fall back to inner store.
	shared := &composition.SharedDeps{Redis: nil}
	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	assert.Same(t, inner, got, "nil Redis with valid TTL should silently return inner store")
}
