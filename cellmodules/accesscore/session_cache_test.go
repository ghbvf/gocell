package accesscore

import (
	"log/slog"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/capability"
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

// TestWrapSessionStoreWithCache_ValidTTLWithRedis_ReturnsCachingStore is the
// happy-path wiring assertion (#795): a valid TTL env + a real Redis provider
// yields a *adapterredis.CachingSessionStore (not the bare inner store), with
// the metrics collector wired. The Redis client is lazily constructed via the
// NewClientForTest seam (never dials — wrapSessionStoreWithCache issues no Redis
// command), and the metrics provider is a Nop. Together they exercise the
// previously-uncovered construction branch end-to-end without a live Redis.
func TestWrapSessionStoreWithCache_ValidTTLWithRedis_ReturnsCachingStore(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "5s")
	inner := newTestSessionMemStore(t)

	// Lazily-connected client; never dialed because the wrap helper performs no
	// Redis I/O (only NewCache + NewCachingSessionStore, both pure constructors).
	redisClient := adapterredis.NewClientForTest(goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"}))
	shared := &composition.SharedDeps{
		Redis:           capability.NewRedisProvider(redisClient),
		MetricsProvider: kernelmetrics.NopProvider{},
	}

	got, err := wrapSessionStoreWithCache(inner, shared, slog.Default())
	require.NoError(t, err)
	require.NotSame(t, inner, got, "valid TTL + Redis must wrap, not return inner")
	_, ok := got.(*adapterredis.CachingSessionStore)
	assert.True(t, ok, "wrapped store must be *adapterredis.CachingSessionStore, got %T", got)
}
