package redis

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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
type fakeSessionStore struct {
	view *session.ValidateView

	getErr        error
	createErr     error
	revokeErr     error
	revokeSubjErr error
	repoReadyErr  error

	getCalls        int
	createCalls     int
	revokeCalls     int
	revokeSubjCalls int
	repoReadyCalls  int
}

func (f *fakeSessionStore) Create(_ context.Context, _ *session.Session) error {
	f.createCalls++
	return f.createErr
}

func (f *fakeSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.view, nil
}

func (f *fakeSessionStore) Revoke(_ context.Context, _ string) error {
	f.revokeCalls++
	return f.revokeErr
}

func (f *fakeSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	f.revokeSubjCalls++
	return f.revokeSubjErr
}

func (f *fakeSessionStore) RepoReady(_ context.Context) error {
	f.repoReadyCalls++
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
	assert.Zero(t, inner.getCalls, "cache hit must not delegate to inner")
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
	assert.Equal(t, 1, inner.getCalls)

	// Cache should now hold the view; second Get must not bump inner.
	got2, err := store.Get(ctx, scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, 1, inner.getCalls, "second Get must hit cache")
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
	assert.Equal(t, 1, inner.getCalls)
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
	assert.Equal(t, 1, inner.getCalls)
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
	assert.Equal(t, 1, inner.createCalls)

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
	assert.Equal(t, 1, inner.revokeCalls)

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
	assert.Equal(t, 1, inner.revokeCalls)
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
	assert.Equal(t, 1, inner.revokeSubjCalls)

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
	assert.Equal(t, 1, inner.repoReadyCalls)
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
	assert.Equal(t, 1, inner.revokeCalls)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.NotNil(t, got.RevokedAt, "Get after Revoke must reflect revoked state, not stale cache")
	assert.Equal(t, 1, inner.getCalls, "Get after Revoke must fall through to inner")
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
