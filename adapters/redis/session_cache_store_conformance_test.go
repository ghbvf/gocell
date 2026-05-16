package redis

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// conformanceCacheTTL is the cache TTL used during conformance runs. It is
// long enough that storetest cases that advance the clock past session
// expiry (Expired_StillReturned) still find the entry present — mockCmdable
// drives TTL from time.Now(), and the suite finishes within microseconds.
const conformanceCacheTTL = time.Hour

// TestConformance_CachingSessionStore_AgainstMem — full Protocol-driven
// session.Store contract suite executed against CachingSessionStore wrapping
// session.MemStore. Proves the cache decorator preserves every invariant the
// inner store guarantees (Create/Get/Revoke/RevokeForSubject semantics +
// epoch round-trip + fingerprint shape).
//
// storetest.Run derives one RevokeForSubject_<Event> case per
// Protocol.RevokeOn() value, so the cache wrapper is exercised against the
// canonical 4-event set declared by NewTestProtocol.
func TestConformance_CachingSessionStore_AgainstMem(t *testing.T) {
	t.Parallel()
	protocol := storetest.NewTestProtocol(t)
	storetest.Run(t, cachingMemFactory(protocol), protocol)
}

// cachingMemFactory closes over the test protocol so each conformance case
// gets a fresh CachingSessionStore wrapping a fresh MemStore + fresh
// mockCmdable. The Factory contract requires returning *FakeClock so suite
// cases can advance it; the cache wrapper holds no clock — the inner store
// does.
func cachingMemFactory(protocol *session.Protocol) storetest.Factory {
	return func(t *testing.T) (session.Store, *clockmock.FakeClock, func()) {
		t.Helper()
		fc := clockmock.New(storetest.EpochAnchor())
		inner, err := session.NewMemStore(protocol, fc)
		if err != nil {
			t.Fatalf("cachingMemFactory: NewMemStore: %v", err)
		}
		cache := mustNewCacheFromCmdable(t, newMockCmdable())
		store, err := NewCachingSessionStore(inner, cache, conformanceCacheTTL, nil)
		if err != nil {
			t.Fatalf("cachingMemFactory: NewCachingSessionStore: %v", err)
		}
		return store, fc, func() {}
	}
}
