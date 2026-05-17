package redis

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

const (
	scsTestTTL     = 30 * time.Second
	scsNegativeTTL = -1 * time.Second
	scsTestSID     = "sess-test-1"
	scsTestSubj    = "subject-A"
	scsTestEpoch   = int64(7)
	scsCachedKey   = "testns:session:sess-test-1"
)

// lastCreateArgs records the arguments passed to the most recent Create call.
type lastCreateArgs struct {
	sess *session.Session
}

// lastGetArgs records the arguments passed to the most recent Get call.
type lastGetArgs struct {
	id string
}

// lastRevokeArgs records the arguments passed to the most recent Revoke call.
type lastRevokeArgs struct {
	id string
}

// lastRevokeSubjArgs records the arguments passed to the most recent RevokeForSubject call.
type lastRevokeSubjArgs struct {
	subjectID string
	event     session.CredentialEvent
}

// fakeSessionStore is an inner session.Store used by CachingSessionStore unit
// tests. It records every call and can inject errors per-method so the
// wrapper's pass-through vs. fail-safe behavior is observable.
//
// T5 fix: call counters use atomic.Int64 so that concurrent tests
// (TestCachingSessionStore_Get_ConcurrentAccess) pass under -race without
// a mutex. Load() replaces direct field reads; Add(1) replaces ++.
//
// lastCreate/lastGet/lastRevoke/lastRevokeSubj capture the most-recent
// call arguments for precise delegation assertions (F2 finding).
type fakeSessionStore struct {
	view *session.ValidateView

	getErr        error
	createErr     error
	revokeErr     error
	revokeSubjErr error
	repoReadyErr  error

	getCalls        atomic.Int64
	createCalls     atomic.Int64
	revokeCalls     atomic.Int64
	revokeSubjCalls atomic.Int64
	repoReadyCalls  atomic.Int64

	lastCreate     atomic.Pointer[lastCreateArgs]
	lastGet        atomic.Pointer[lastGetArgs]
	lastRevoke     atomic.Pointer[lastRevokeArgs]
	lastRevokeSubj atomic.Pointer[lastRevokeSubjArgs]
}

func (f *fakeSessionStore) Create(_ context.Context, sess *session.Session) error {
	f.createCalls.Add(1)
	f.lastCreate.Store(&lastCreateArgs{sess: sess})
	return f.createErr
}

func (f *fakeSessionStore) Get(_ context.Context, id string) (*session.ValidateView, error) {
	f.getCalls.Add(1)
	f.lastGet.Store(&lastGetArgs{id: id})
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.view, nil
}

func (f *fakeSessionStore) Revoke(_ context.Context, id string) error {
	f.revokeCalls.Add(1)
	f.lastRevoke.Store(&lastRevokeArgs{id: id})
	return f.revokeErr
}

func (f *fakeSessionStore) RevokeForSubject(_ context.Context, subjectID string, event session.CredentialEvent) error {
	f.revokeSubjCalls.Add(1)
	f.lastRevokeSubj.Store(&lastRevokeSubjArgs{subjectID: subjectID, event: event})
	return f.revokeSubjErr
}

func (f *fakeSessionStore) RepoReady(_ context.Context) error {
	f.repoReadyCalls.Add(1)
	return f.repoReadyErr
}

func newTestView() *session.ValidateView {
	return &session.ValidateView{
		ID:                scsTestSID,
		SubjectID:         scsTestSubj,
		RevokedAt:         nil,
		AuthzEpochAtIssue: scsTestEpoch,
	}
}

func newTestCachingStore(t *testing.T, inner session.Store, mock *mockCmdable) *CachingSessionStore {
	t.Helper()
	cache := mustNewCacheFromCmdable(t, mock)
	store, err := NewCachingSessionStore(inner, cache, scsTestTTL, nil)
	if err != nil {
		t.Fatalf("NewCachingSessionStore: %v", err)
	}
	return store
}

// TestNewCachingSessionStore_FailFast — construction rejects nil inner, nil
// cache, or non-positive ttl with errcode.ErrValidationFailed (Hard,
// composition-root fail-fast).
func TestNewCachingSessionStore_FailFast(t *testing.T) {
	t.Parallel()
	cache := mustNewCacheFromCmdable(t, newMockCmdable())
	inner := &fakeSessionStore{view: newTestView()}

	cases := []struct {
		name  string
		inner session.Store
		cache *Cache
		ttl   time.Duration
	}{
		{name: "nil_inner", inner: nil, cache: cache, ttl: scsTestTTL},
		{name: "nil_cache", inner: inner, cache: nil, ttl: scsTestTTL},
		{name: "zero_ttl", inner: inner, cache: cache, ttl: 0},
		{name: "negative_ttl", inner: inner, cache: cache, ttl: scsNegativeTTL},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store, err := NewCachingSessionStore(c.inner, c.cache, c.ttl, nil)
			assert.Nil(t, store)
			var coded *errcode.Error
			require.ErrorAs(t, err, &coded)
			assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
		})
	}
}

// TestCachingSessionStore_Get_CacheHit — when Redis already holds the view,
// inner.Get is never invoked.
func TestCachingSessionStore_Get_CacheHit(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	view := newTestView()
	payload, err := json.Marshal(entryFromView(view))
	require.NoError(t, err)
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())

	inner := &fakeSessionStore{view: view}
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	assert.Equal(t, view.ID, got.ID)
	assert.Equal(t, view.SubjectID, got.SubjectID)
	assert.Equal(t, view.AuthzEpochAtIssue, got.AuthzEpochAtIssue)
	assert.Zero(t, inner.getCalls.Load(), "cache hit must not delegate to inner")
}

// TestCachingSessionStore_Get_CacheMiss_PrimesCache — first Get misses the
// cache, calls inner, and lazily populates. Subsequent Get hits cache and
// inner.getCalls stays at 1.
func TestCachingSessionStore_Get_CacheMiss_PrimesCache(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStore(t, inner, mock)
	ctx := context.Background()

	got, err := store.Get(ctx, scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(1), inner.getCalls.Load())
	if args := inner.lastGet.Load(); assert.NotNil(t, args, "lastGet must be set") {
		assert.Equal(t, scsTestSID, args.id, "Get must delegate exact id")
	}

	// Cache should now hold the view; second Get must not bump inner.
	got2, err := store.Get(ctx, scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, int64(1), inner.getCalls.Load(), "second Get must hit cache")
}

// TestCachingSessionStore_Get_RedisDown_FallsThrough — cache.Get returns an
// error: wrapper logs and falls through to inner without surfacing the cache
// failure to the caller.
func TestCachingSessionStore_Get_RedisDown_FallsThrough(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	mock.getErr = errors.New("redis: connection refused")
	mock.setErr = errors.New("redis: connection refused") // also fail the lazy populate path
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, int64(1), inner.getCalls.Load())
}

// TestCachingSessionStore_Get_CorruptCacheEntry_FallsThrough — cache returns
// non-JSON, wrapper logs and falls through.
func TestCachingSessionStore_Get_CorruptCacheEntry_FallsThrough(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, "<<not-json>>", scsTestTTL).Err())
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, int64(1), inner.getCalls.Load())
}

// TestCachingSessionStore_Get_InnerError_PropagatesAsInfra — inner returns a
// transient KindUnavailable; wrapper propagates the error unchanged so
// sessionvalidate can render 503.
func TestCachingSessionStore_Get_InnerError_PropagatesAsInfra(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	transientErr := errcode.WrapInfra(errcode.ErrAuthServiceUnavailable,
		"session store unavailable", errors.New("pg conn refused"))
	inner := &fakeSessionStore{getErr: transientErr}
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	assert.Nil(t, got)
	require.Error(t, err)
	assert.True(t, errcode.IsInfraError(err), "infra error must propagate as-is")
}

// TestCachingSessionStore_Create_DoesNotTouchCache — Create only delegates;
// cache must receive no Set call (and remain empty).
func TestCachingSessionStore_Create_DoesNotTouchCache(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{}
	store := newTestCachingStore(t, inner, mock)

	sess := &session.Session{ID: scsTestSID, SubjectID: scsTestSubj, JTI: "jti-x", AuthzEpochAtIssue: scsTestEpoch}
	require.NoError(t, store.Create(context.Background(), sess))
	assert.Equal(t, int64(1), inner.createCalls.Load())
	if args := inner.lastCreate.Load(); assert.NotNil(t, args, "lastCreate must be set") {
		assert.Equal(t, scsTestSID, args.sess.ID, "Create must delegate exact sess")
	}

	// The mock store must remain empty.
	cmd := mock.Get(context.Background(), scsCachedKey)
	_, err := cmd.Result()
	assert.Error(t, err, "cache must be untouched after Create")
}

// TestCachingSessionStore_Revoke_DoesNotTouchCache — Revoke delegates to
// inner only; cache must remain untouched (TTL-floor contract, see type godoc
// §Threat model and archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01).
//
// Symmetric with TestCachingSessionStore_RevokeForSubject_DoesNotTouchCache:
// both Revoke and RevokeForSubject rely on the cache TTL as the security
// floor, not in-transaction Redis DEL. The TTL is bounded at max 30s by
// wrapSessionStoreWithCache (wiring-time fail-fast).
func TestCachingSessionStore_Revoke_DoesNotTouchCache(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	view := newTestView()
	payload, err := json.Marshal(entryFromView(view))
	require.NoError(t, err)
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())

	inner := &fakeSessionStore{view: view}
	store := newTestCachingStore(t, inner, mock)

	require.NoError(t, store.Revoke(context.Background(), scsTestSID))
	assert.Equal(t, int64(1), inner.revokeCalls.Load())
	if args := inner.lastRevoke.Load(); assert.NotNil(t, args, "lastRevoke must be set") {
		assert.Equal(t, scsTestSID, args.id, "Revoke must delegate exact id")
	}

	// Cache entry must still be present — wrapper must not have invalidated.
	cmd := mock.Get(context.Background(), scsCachedKey)
	val, err := cmd.Result()
	require.NoError(t, err, "cache entry must still exist after Revoke (TTL-floor contract)")
	assert.NotEmpty(t, val)
}

// TestCachingSessionStore_Revoke_InnerError_Propagates — inner errors flow
// through unchanged; no cache operation is attempted (TTL-floor contract per
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01).
func TestCachingSessionStore_Revoke_InnerError_Propagates(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	innerErr := errcode.WrapInfra(errcode.ErrAuthServiceUnavailable,
		"session store unavailable", errors.New("pg conn refused"))
	inner := &fakeSessionStore{revokeErr: innerErr}
	store := newTestCachingStore(t, inner, mock)

	err := store.Revoke(context.Background(), scsTestSID)
	require.Error(t, err)
	assert.True(t, errcode.IsInfraError(err))
}

// TestCachingSessionStore_RevokeForSubject_DoesNotTouchCache — cache layer
// receives no Del / Get; only inner is invoked. Defends the epoch-fallback
// contract: after RevokeForSubject the cached ValidateView's AuthzEpochAtIssue
// is compared by sessionvalidate.go against the live user.AuthzEpoch (bumped
// co-tx by credentialinvalidate.Apply); mismatch → 401 fail-closed regardless
// of cache state. See ADR docs/architecture/202605101400-adr-credential-
// session-protocol.md §A8 and archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
//
// Symmetric with TestCachingSessionStore_Revoke_DoesNotTouchCache: from the
// third-round review both Revoke and RevokeForSubject are pure inner delegates
// with no cache operations. The cache TTL (max 30s) is the security floor for
// both paths.
func TestCachingSessionStore_RevokeForSubject_DoesNotTouchCache(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	view := newTestView()
	payload, err := json.Marshal(entryFromView(view))
	require.NoError(t, err)
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())
	inner := &fakeSessionStore{}
	store := newTestCachingStore(t, inner, mock)

	err = store.RevokeForSubject(context.Background(), scsTestSubj, session.CredentialEventPasswordReset)
	require.NoError(t, err)
	assert.Equal(t, int64(1), inner.revokeSubjCalls.Load())
	if args := inner.lastRevokeSubj.Load(); assert.NotNil(t, args, "lastRevokeSubj must be set") {
		assert.Equal(t, scsTestSubj, args.subjectID, "RevokeForSubject must delegate exact subjectID")
		assert.Equal(t, session.CredentialEventPasswordReset, args.event, "RevokeForSubject must delegate exact event")
	}

	// Cache entry must still be present — wrapper must not have invalidated.
	cmd := mock.Get(context.Background(), scsCachedKey)
	val, err := cmd.Result()
	require.NoError(t, err, "cache entry must still exist after RevokeForSubject")
	assert.NotEmpty(t, val)
}

// TestCachingSessionStore_RepoReady_Delegates — repo readiness is the inner's
// concern; Redis liveness is covered by the adapter-level redis_ready probe.
func TestCachingSessionStore_RepoReady_Delegates(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{repoReadyErr: errors.New("schema drift")}
	store := newTestCachingStore(t, inner, mock)

	err := store.RepoReady(context.Background())
	require.Error(t, err)
	assert.Equal(t, int64(1), inner.repoReadyCalls.Load())
}

// TestCachingSessionStore_Get_NilViewFromInner_DoesNotPopulate — inner may
// theoretically return (nil, nil); the wrapper must not write a nil entry.
func TestCachingSessionStore_Get_NilViewFromInner_DoesNotPopulate(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{view: nil} // (nil, nil) round
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	assert.Nil(t, got)

	cmd := mock.Get(context.Background(), scsCachedKey)
	_, err = cmd.Result()
	assert.Error(t, err, "nil view must not be cached")
}

// TestCachingSessionStore_Get_ConcurrentAccess — exercises the read-through
// hot path under concurrent goroutines so `go test -race` would surface any
// data race in the wrapper's internals (lazy populate writeback, in
// particular).
func TestCachingSessionStore_Get_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStore(t, inner, mock)

	const goroutines = 16
	const iters = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for j := 0; j < iters; j++ {
				got, err := store.Get(ctx, scsTestSID)
				if err != nil {
					t.Errorf("Get error under concurrency: %v", err)
					return
				}
				if got == nil {
					t.Errorf("Get returned nil under concurrency")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestCachingSessionStore_Get_InvalidCachedEntry_FallsThrough verifies that
// the Get read-through falls back to inner.Get for any cached entry that fails
// validation. Two distinct code paths are exercised:
//
//   - json-unmarshal path: the stored value is not valid JSON (or parses to a
//     type that cannot be assigned to sessionCacheEntry). json.Unmarshal returns
//     a non-nil jerr — the wrapper logs "corrupt cached entry" and falls through.
//
//   - validate path: json.Unmarshal succeeds but entry.validate(id) rejects the
//     entry because of a field-level invariant violation (mismatched ID, empty
//     SubjectID, zero AuthzEpochAtIssue, or revoked entry). The wrapper logs
//     "invalid cached entry" and falls through.
//
// In both paths Get must: (1) fall through to inner.Get (inner.getCalls == 1),
// (2) return the view from inner, and (3) not surface the bad cache entry.
func TestCachingSessionStore_Get_InvalidCachedEntry_FallsThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
	}{
		// ── json-unmarshal path ──────────────────────────────────────────────────
		{
			name:    "not_json", // json-unmarshal path: jerr != nil
			payload: `<<not-json>>`,
		},
		// ── validate path ────────────────────────────────────────────────────────
		{
			name:    "mismatched_id", // validate-path: ID != wantID
			payload: `{"id":"different-sid","subjectId":"subject-A","authzEpochAtIssue":7}`,
		},
		{
			name:    "empty_subject_id", // validate-path: empty SubjectID
			payload: `{"id":"sess-test-1","subjectId":"","authzEpochAtIssue":7}`,
		},
		{
			name:    "zero_epoch", // validate-path: non-positive AuthzEpochAtIssue
			payload: `{"id":"sess-test-1","subjectId":"subject-A","authzEpochAtIssue":0}`,
		},
		{
			name:    "empty_object", // validate-path: empty SubjectID (zero-value struct)
			payload: `{}`,
		},
		{
			name:    "json_null", // validate-path: empty SubjectID (null → zero-value struct)
			payload: `null`,
		},
		{
			name:    "revoked_view_in_cache", // validate-path: RevokedAt != nil
			payload: `{"id":"sess-test-1","subjectId":"subject-A","authzEpochAtIssue":7,"revokedAt":"2026-05-17T00:00:00Z"}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockCmdable()
			// Prime the cache with the invalid payload.
			require.NoError(t,
				mock.Set(context.Background(), scsCachedKey, tc.payload, scsTestTTL).Err(),
				"setup: prime cache with invalid payload")

			// inner returns a known-good view.
			innerView := newTestView()
			inner := &fakeSessionStore{view: innerView}
			store := newTestCachingStore(t, inner, mock)

			got, err := store.Get(context.Background(), scsTestSID)
			require.NoError(t, err, "Get must not return an error for invalid cache entry")

			// Must fall through to inner.
			assert.Equal(t, int64(1), inner.getCalls.Load(),
				"invalid cache entry must not prevent inner.Get call (fall-through required)")

			// Result must come from inner, not from the bad cache entry.
			require.NotNil(t, got, "Get must return the inner view after cache miss/fallthrough")
			assert.Equal(t, scsTestSID, got.ID,
				"returned view must have correct ID from inner")
			assert.Equal(t, scsTestSubj, got.SubjectID,
				"returned view must have correct SubjectID from inner")
			assert.Equal(t, scsTestEpoch, got.AuthzEpochAtIssue,
				"returned view must have correct AuthzEpochAtIssue from inner")
		})
	}
}

// TestCachingSessionStore_Get_RevokedViewFromInner_DoesNotPopulate — inner may
// return a revoked view (RevokedAt != nil); the wrapper must return it to the
// caller (inner result is always authoritative) but must NOT write it into the
// cache. Mirrors TestCachingSessionStore_Get_NilViewFromInner_DoesNotPopulate.
func TestCachingSessionStore_Get_RevokedViewFromInner_DoesNotPopulate(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	now := time.Now()
	revokedView := &session.ValidateView{
		ID:                scsTestSID,
		SubjectID:         scsTestSubj,
		RevokedAt:         &now,
		AuthzEpochAtIssue: scsTestEpoch,
	}
	inner := &fakeSessionStore{view: revokedView}
	store := newTestCachingStore(t, inner, mock)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.NotNil(t, got.RevokedAt, "returned view must preserve RevokedAt from inner")

	cmd := mock.Get(context.Background(), scsCachedKey)
	_, err = cmd.Result()
	assert.Error(t, err, "revoked view must not be written to cache")
}

// TestCachingSessionStore_Get_CorruptCacheEntry_DeletesAndFallsThrough — F4 RED test.
//
// When the cache holds a corrupt (non-JSON) entry, Get must:
//  1. Fall through to inner.Get (inner.getCalls == 1).
//  2. Synchronously delete the corrupt entry so the second Get does not
//     re-parse the same corrupt bytes. The second Get must go to cache
//     (populated by the first call's lazyPopulate) not inner again.
//
// The current code falls through (step 1) but does NOT delete the corrupt
// entry (step 2 missing). Without DELETE, a second Get would encounter the
// same corrupt bytes again and fall through to inner a second time.
// This test FAILS on the pre-GREEN codebase — that is the intentional RED
// state. The GREEN fix adds a synchronous cache.Delete in the unmarshal-fail
// branch, after which lazyPopulate re-primes the cache with a valid entry.
func TestCachingSessionStore_Get_CorruptCacheEntry_DeletesAndFallsThrough(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, "<<not-json>>", scsTestTTL).Err())
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStore(t, inner, mock)

	ctx := context.Background()
	got, err := store.Get(ctx, scsTestSID)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, int64(1), inner.getCalls.Load(), "corrupt cache entry must fall through to inner")

	// After the first Get, the corrupt entry was deleted and lazyPopulate
	// re-primed the cache with a valid entry. A second Get must hit the cache
	// (inner.getCalls stays at 1) — if corrupt entry was NOT deleted, the second
	// Get would again encounter the corrupt bytes and fall through to inner
	// (inner.getCalls would be 2).
	got2, err2 := store.Get(ctx, scsTestSID)
	require.NoError(t, err2)
	assert.NotNil(t, got2)
	assert.Equal(t, int64(1), inner.getCalls.Load(),
		"second Get must hit the re-primed cache (corrupt entry was deleted and lazyPopulated with valid entry); "+
			"inner.getCalls==2 means corrupt entry was NOT deleted")
}

// TestNewCachingSessionStore_TypedNilInner_Rejected — T3.
//
// A typed-nil session.Store interface (concrete type is non-nil, pointer value
// is nil) must be rejected at construction time with errcode.ErrValidationFailed.
// NewCachingSessionStore uses validation.IsNilInterface(inner) so a typed-nil
// fails the constructor instead of deferring a nil-pointer panic to first use.
func TestNewCachingSessionStore_TypedNilInner_Rejected(t *testing.T) {
	t.Parallel()
	cache := mustNewCacheFromCmdable(t, newMockCmdable())

	// Construct a typed-nil: interface is non-nil (has a type), pointer value is nil.
	var typedNilInner session.Store = (*fakeSessionStore)(nil)

	store, err := NewCachingSessionStore(typedNilInner, cache, scsTestTTL, nil)

	assert.Nil(t, store, "constructor must return nil store for typed-nil inner")
	require.Error(t, err, "constructor must return an error for typed-nil inner")

	var coded *errcode.Error
	require.ErrorAs(t, err, &coded,
		"error must be *errcode.Error for typed-nil inner; got %T: %v", err, err)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code,
		"error code must be ErrValidationFailed for typed-nil inner")
}
