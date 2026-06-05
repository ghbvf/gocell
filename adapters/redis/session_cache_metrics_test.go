package redis

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// spyCacheMetrics is an in-package spy for the cacheMetricsRecorder seam so the
// decorator's hit/miss/error wiring is observable without a real metrics
// provider.
type spyCacheMetrics struct {
	hits, misses, errs int
}

func (s *spyCacheMetrics) RecordHit(context.Context)   { s.hits++ }
func (s *spyCacheMetrics) RecordMiss(context.Context)  { s.misses++ }
func (s *spyCacheMetrics) RecordError(context.Context) { s.errs++ }

func newTestCachingStoreWithMetrics(
	t *testing.T, inner session.Store, mock *mockCmdable, rec cacheMetricsRecorder,
) *CachingSessionStore {
	t.Helper()
	cache := mustNewCacheFromCmdable(t, mock)
	store, err := NewCachingSessionStore(inner, cache, scsTestTTL, nil, rec)
	require.NoError(t, err)
	return store
}

// TestCachingSessionStore_Metrics_Hit — a clean cache hit records exactly one
// hit, no miss, no error, and does not consult the inner store.
func TestCachingSessionStore_Metrics_Hit(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	view := newTestView()
	payload, err := json.Marshal(entryFromView(view))
	require.NoError(t, err)
	require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())

	rec := &spyCacheMetrics{}
	inner := &fakeSessionStore{view: view}
	store := newTestCachingStoreWithMetrics(t, inner, mock, rec)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 1, rec.hits)
	assert.Equal(t, 0, rec.misses)
	assert.Equal(t, 0, rec.errs)
	assert.Equal(t, int64(0), inner.getCalls.Load(), "hit must not consult inner")
}

// TestCachingSessionStore_Metrics_Miss — an empty cache records exactly one
// miss (no hit, no error) and falls through to the inner store.
func TestCachingSessionStore_Metrics_Miss(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	rec := &spyCacheMetrics{}
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStoreWithMetrics(t, inner, mock, rec)

	got, err := store.Get(context.Background(), scsTestSID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 0, rec.hits)
	assert.Equal(t, 1, rec.misses)
	assert.Equal(t, 0, rec.errs)
	assert.Equal(t, int64(1), inner.getCalls.Load(), "miss must consult inner")
}

// TestCachingSessionStore_Metrics_Errors — table over the cache-access error
// sites. Each records exactly one error AND one miss (errors ⊆ misses: a Get
// that hit an error still did not serve from cache), and falls through safely.
func TestCachingSessionStore_Metrics_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, mock *mockCmdable)
	}{
		{
			name: "redis_get_error",
			setup: func(_ *testing.T, mock *mockCmdable) {
				mock.getErr = errMock
			},
		},
		{
			name: "corrupt_json",
			setup: func(t *testing.T, mock *mockCmdable) {
				require.NoError(t, mock.Set(context.Background(), scsCachedKey, "not-json", scsTestTTL).Err())
			},
		},
		{
			name: "populate_set_error",
			setup: func(_ *testing.T, mock *mockCmdable) {
				mock.setErr = errMock // miss → inner → lazyPopulate Set fails
			},
		},
		{
			// schema_invalid: valid JSON but entry.validate() rejects it because
			// the stored entry.ID does not match the requested session ID. This
			// exercises the evictBadEntry "invalid" branch → RecordError + RecordMiss.
			name: "schema_invalid",
			setup: func(t *testing.T, mock *mockCmdable) {
				t.Helper()
				// Construct an entry whose ID deliberately mismatches the requested
				// session ID (scsTestSID = "sess-test-1") so validate() fails.
				badEntry := sessionCacheEntry{
					ID:                "wrong-id",
					SubjectID:         scsTestSubj,
					AuthzEpochAtIssue: scsTestEpoch,
				}
				payload, err := json.Marshal(badEntry)
				require.NoError(t, err)
				require.NoError(t, mock.Set(context.Background(), scsCachedKey, string(payload), scsTestTTL).Err())
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockCmdable()
			c.setup(t, mock)
			rec := &spyCacheMetrics{}
			inner := &fakeSessionStore{view: newTestView()}
			store := newTestCachingStoreWithMetrics(t, inner, mock, rec)

			_, err := store.Get(context.Background(), scsTestSID)
			require.NoError(t, err, "cache errors are fail-safe and must not propagate")
			assert.Equal(t, 1, rec.errs, "exactly one error recorded")
			assert.Equal(t, 1, rec.misses, "error path is also a miss (errors ⊆ misses)")
			assert.Equal(t, 0, rec.hits)
		})
	}
}

// TestCachingSessionStore_Metrics_NilRecorder — a cache constructed with a nil
// recorder (metrics disabled) must not panic on the hot path.
func TestCachingSessionStore_Metrics_NilRecorder(t *testing.T) {
	t.Parallel()
	mock := newMockCmdable()
	inner := &fakeSessionStore{view: newTestView()}
	store := newTestCachingStoreWithMetrics(t, inner, mock, nil)
	assert.NotPanics(t, func() {
		_, _ = store.Get(context.Background(), scsTestSID)
	})
}
