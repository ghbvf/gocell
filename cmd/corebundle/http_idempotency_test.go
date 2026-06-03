package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/composition"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// TestBuildHTTPIdempotencyStore_NilClientReturnsNilNil verifies that a nil
// client short-circuits construction and returns (nil, nil) — the wiring in
// defaultRuntimeOptions checks for a nil store before calling
// WithIdempotencyStore to avoid the typed-nil rejection.
func TestBuildHTTPIdempotencyStore_NilClientReturnsNilNil(t *testing.T) {
	t.Parallel()
	store, err := buildHTTPIdempotencyStore(nil)
	require.NoError(t, err)
	assert.Nil(t, store, "nil client must return nil store")
}

// TestBuildHTTPIdempotencyStore_NonNilClientReturnsStore verifies that a
// non-nil client produces a non-nil Store suitable for WithIdempotencyStore.
// The factory var is overridden to avoid real Redis I/O (zero-value Client
// has no real cmdable), matching the pattern used by restoreRedisClaimerFactory.
func TestBuildHTTPIdempotencyStore_NonNilClientReturnsStore(t *testing.T) {
	orig := newRedisHTTPIdempotencyStore
	newRedisHTTPIdempotencyStore = func(_ *adapterredis.Client, _ adapterredis.KeyNamespace) (idemhttp.Store, error) {
		return idemhttp.NewMemStore(clock.Real()), nil
	}
	t.Cleanup(func() { newRedisHTTPIdempotencyStore = orig })

	store, err := buildHTTPIdempotencyStore(new(adapterredis.Client))
	require.NoError(t, err)
	require.NotNil(t, store, "non-nil client must produce a non-nil Store")
}

// TestBuildHTTPIdempotencyStore_FactoryErrorPropagated verifies that when the
// factory var newRedisHTTPIdempotencyStore returns an error it is propagated
// with wrapping context from buildHTTPIdempotencyStore.
func TestBuildHTTPIdempotencyStore_FactoryErrorPropagated(t *testing.T) {
	wantErr := errors.New("redis http idempotency factory failed")
	orig := newRedisHTTPIdempotencyStore
	newRedisHTTPIdempotencyStore = func(_ *adapterredis.Client, _ adapterredis.KeyNamespace) (idemhttp.Store, error) {
		return nil, wantErr
	}
	t.Cleanup(func() { newRedisHTTPIdempotencyStore = orig })

	_, err := buildHTTPIdempotencyStore(new(adapterredis.Client))
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "build Redis HTTP idempotency store")
}

// ---------------------------------------------------------------------------
// defaultRuntimeOptions wiring tests
// ---------------------------------------------------------------------------

// buildIdemTestAsm returns a minimal assembly and ConsumerBase for wiring tests
// that call defaultRuntimeOptions. The assembly must have DurabilityDemo so
// tests do not need a real AMQP broker.
func buildIdemTestAsm(t *testing.T, shared *composition.SharedDeps) (*assembly.CoreAssembly, *outbox.ConsumerBase) {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-idem-wiring", DurabilityMode: outbox.DurabilityDemo})
	cb, err := buildConsumerBase(shared)
	require.NoError(t, err)
	return asm, cb
}

// TestDefaultRuntimeOptions_NoRedisNoIdempotencyOption verifies that when
// locals.redisClient is nil (memory/single-pod mode) no idempotency option is
// appended — WithIdempotencyStore must not be called with nil.
func TestDefaultRuntimeOptions_NoRedisNoIdempotencyOption(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	shared.InternalHTTPAddr = "127.0.0.1:0"
	locals.internalGuard = newTestInternalGuard(t)
	shared.InternalHMACRing = locals.internalGuard.ring
	asm, cb := buildIdemTestAsm(t, shared)

	// Override factory to prevent any accidental call.
	orig := newRedisHTTPIdempotencyStore
	called := false
	newRedisHTTPIdempotencyStore = func(_ *adapterredis.Client, _ adapterredis.KeyNamespace) (idemhttp.Store, error) {
		called = true
		return idemhttp.NewMemStore(clock.Real()), nil
	}
	t.Cleanup(func() { newRedisHTTPIdempotencyStore = orig })

	// locals.redisClient is nil — idempotency store factory must not be called.
	opts, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.NoError(t, err)
	assert.False(t, called, "factory must not be called when redisClient is nil")
	_ = opts // count not asserted here; just verifies no panic / no error
}

// TestDefaultRuntimeOptions_WithRedisAddsIdempotencyOption verifies that when
// locals.redisClient is non-nil (Redis-backed topology) defaultRuntimeOptions
// appends exactly one extra bootstrap.Option compared to the nil-client path.
// The extra option is the WithIdempotencyStore call.
func TestDefaultRuntimeOptions_WithRedisAddsIdempotencyOption(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	shared.InternalHTTPAddr = "127.0.0.1:0"
	locals.internalGuard = newTestInternalGuard(t)
	shared.InternalHMACRing = locals.internalGuard.ring
	asm, cb := buildIdemTestAsm(t, shared)

	// Override factory to inject a MemStore without a real Redis connection.
	orig := newRedisHTTPIdempotencyStore
	newRedisHTTPIdempotencyStore = func(_ *adapterredis.Client, _ adapterredis.KeyNamespace) (idemhttp.Store, error) {
		return idemhttp.NewMemStore(clock.Real()), nil
	}
	t.Cleanup(func() { newRedisHTTPIdempotencyStore = orig })

	// Without Redis client.
	optsBase, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.NoError(t, err)

	// Set a non-nil Redis client — also set shared.Redis so the ManagedResource
	// block in runtimeBaseOptions also fires (it adds 1). We want to isolate the
	// idempotency option count as exactly one additional on top of that.
	locals.redisClient = new(adapterredis.Client)
	shared.Redis = capability.NewRedisProvider(new(adapterredis.Client))
	optsWithRedis, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.NoError(t, err)

	// Adding Redis adds THREE options in total over the base:
	//   1. bootstrap.WithManagedResource (Redis client close, from runtimeBaseOptions)
	//   2. bootstrap.WithIdempotencyStore (new in this PR)
	//   3. bootstrap.WithHealthChecker (http_idempotency_store_ready — MemStore here
	//      does not implement ReadyCheck, so this is NOT appended in this test;
	//      hence +2). The probe wiring is covered by the redis-store ReadyCheck unit
	//      tests in adapters/redis.
	assert.Len(t, optsWithRedis, len(optsBase)+2,
		"adding Redis must append WithManagedResource + WithIdempotencyStore (MemStore has no ReadyCheck)")
}

// TestDefaultRuntimeOptions_RedisPresentButNilStore_FailsClosed verifies the
// F1 fail-closed guard (#1537 review): when Redis is configured but the store
// factory returns (nil, nil), defaultRuntimeOptions must REFUSE to start rather
// than silently leave default-on idempotency inactive.
func TestDefaultRuntimeOptions_RedisPresentButNilStore_FailsClosed(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	shared.InternalHTTPAddr = "127.0.0.1:0"
	locals.internalGuard = newTestInternalGuard(t)
	shared.InternalHMACRing = locals.internalGuard.ring
	asm, cb := buildIdemTestAsm(t, shared)

	// Simulate a fail-open factory: non-nil client but nil store, nil error.
	orig := newRedisHTTPIdempotencyStore
	newRedisHTTPIdempotencyStore = func(_ *adapterredis.Client, _ adapterredis.KeyNamespace) (idemhttp.Store, error) {
		return nil, nil //nolint:nilnil // deliberately exercise the fail-open return the guard must reject
	}
	t.Cleanup(func() { newRedisHTTPIdempotencyStore = orig })

	locals.redisClient = new(adapterredis.Client)
	shared.Redis = capability.NewRedisProvider(new(adapterredis.Client))

	_, err := defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	require.Error(t, err, "Redis present + nil store must fail-closed, not silently skip idempotency")
	assert.Contains(t, err.Error(), "fail-closed")
}
