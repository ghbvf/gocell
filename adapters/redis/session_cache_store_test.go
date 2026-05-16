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

// fakeSessionStore is an inner session.Store used by CachingSessionStore unit
// tests. It records every call and can inject errors per-method so the
// wrapper's pass-through vs. fail-safe behavior is observable.
//
// T5 fix: call counters use atomic.Int64 so that concurrent tests
// (TestCachingSessionStore_Get_ConcurrentAccess) pass under -race without
// a mutex. Load() replaces direct field reads; Add(1) replaces ++.
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
}

func (f *fakeSessionStore) Create(_ context.Context, _ *session.Session) error {
	f.createCalls.Add(1)
	return f.createErr
}

func (f *fakeSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	f.getCalls.Add(1)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.view, nil
}

func (f *fakeSessionStore) Revoke(_ context.Context, _ string) error {
	f.revokeCalls.Add(1)
	return f.revokeErr
}

func (f *fakeSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	f.revokeSubjCalls.Add(1)
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

	// The mock store must remain empty.
	cmd := mock.Get(context.Background(), scsCachedKey)
	_, err := cmd.Result()
	assert.Error(t, err, "cache must be untouched after Create")
}

// TestCachingSessionStore_Revoke_DeletesCache — after revoke, the cache entry
// is gone so the next Get falls through to inner.
func TestCachingSessionStore_Revoke_DeletesCache(t *testing.T) {
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

	// Cache entry must be gone.
	cmd := mock.Get(context.Background(), scsCachedKey)
	_, err = cmd.Result()
	assert.Error(t, err, "cache entry should be deleted after Revoke")
}

// TestCachingSessionStore_Revoke_RedisDown_StillSucceeds — cache.Delete error
// is swallowed; inner.Revoke return value drives the wrapper's return.
func TestCachingSessionStore_Revoke_RedisDown_StillSucceeds(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	mock.delErr = errors.New("redis: connection refused")
	inner := &fakeSessionStore{}
	store := newTestCachingStore(t, inner, mock)

	require.NoError(t, store.Revoke(context.Background(), scsTestSID))
	assert.Equal(t, int64(1), inner.revokeCalls.Load())
}

// TestCachingSessionStore_Revoke_InnerError_Propagates — inner errors flow
// through unchanged; cache.Delete still attempted (fail-safe order).
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
// invariant from the plan.
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

// TestCachingSessionStore_Revoke_FollowedByGet_FallsThroughToInner — defends
// the security path: after Revoke the next Get must not return a stale view
// from cache. This is the single-session-revoke variant of the safety
// argument used in the type godoc.
func TestCachingSessionStore_Revoke_FollowedByGet_FallsThroughToInner(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	view := newTestView()
	payload, err := json.Marshal(entryFromView(view))
	require.NoError(t, err)
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())

	// Inner reports the revoked view after Revoke.
	revokedAt := time.Now().UTC()
	revokedView := &session.ValidateView{
		ID:                view.ID,
		SubjectID:         view.SubjectID,
		RevokedAt:         &revokedAt,
		AuthzEpochAtIssue: view.AuthzEpochAtIssue,
	}
	inner := &fakeSessionStore{view: revokedView}
	store := newTestCachingStore(t, inner, mock)

	require.NoError(t, store.Revoke(context.Background(), scsTestSID))
	assert.Equal(t, int64(1), inner.revokeCalls.Load())

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.NotNil(t, got.RevokedAt, "Get after Revoke must reflect revoked state, not stale cache")
	assert.Equal(t, int64(1), inner.getCalls.Load(), "Get after Revoke must fall through to inner")
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

// TestCachingSessionStore_Get_InvalidCachedEntry_FallsThrough — T2 RED test.
//
// When the Redis cache holds a well-formed JSON payload that passes
// json.Unmarshal but contains a semantically invalid cached entry (mismatched
// ID, empty SubjectID, zero AuthzEpochAtIssue, empty object, or JSON null),
// the Get method must:
//  1. fall through to inner.Get (inner.getCalls == 1)
//  2. return the view from inner, not from the invalid cache
//  3. not return the corrupt cache entry as the result
//
// Current code (session_cache_store.go:160-163) returns any successfully
// unmarshalled entry without validation, so all five cases FAIL — that is the
// intentional RED state. The GREEN fix will add an entry.validate() helper.
func TestCachingSessionStore_Get_InvalidCachedEntry_FallsThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "mismatched_id",
			payload: `{"id":"different-sid","subjectId":"subject-A","authzEpochAtIssue":7}`,
		},
		{
			name:    "empty_subject_id",
			payload: `{"id":"sess-test-1","subjectId":"","authzEpochAtIssue":7}`,
		},
		{
			name:    "zero_epoch",
			payload: `{"id":"sess-test-1","subjectId":"subject-A","authzEpochAtIssue":0}`,
		},
		{
			name:    "empty_object",
			payload: `{}`,
		},
		{
			name:    "json_null",
			payload: `null`,
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

// TestNewCachingSessionStore_TypedNilInner_Rejected — T3 RED test.
//
// A typed-nil session.Store interface (concrete type is non-nil, pointer value
// is nil) must be rejected at construction time with errcode.ErrValidationFailed.
//
// Current code (session_cache_store.go:125) uses `inner == nil` which evaluates
// to false for typed-nil interfaces — the nil-pointer panic is deferred to the
// first method call. This test FAILS on develop tip — that is the intentional
// RED state. The GREEN fix replaces the check with validation.IsNilInterface(inner).
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
