package redis

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CachingSessionStore is a read-through Redis cache decorator over a
// runtime/auth/session.Store. The wrapped (inner) store remains the system of
// record; the Redis cache only accelerates Get and is invalidated on single-
// session Revoke. Construction-time fail-fast ensures wiring misconfigurations
// surface at startup, not at the first request.
//
// Behavior summary (T5/AUTH-CACHE-01 plan):
//
//   - Get: cache hit → unmarshal *ValidateView and return. Miss / Redis error
//     / corrupt JSON → slog.Warn + fall through to inner.Get; on success the
//     view is lazily populated into the cache (Set error is also swallowed).
//   - Create: delegated to inner. No cache write — the first Get after Create
//     primes the cache via the read-through path (avoids "created then revoked"
//     edge thinking and removes a method's worth of code).
//   - Revoke: inner.Revoke then cache.Delete. Delete is always attempted
//     (idempotent) even if the inner call surfaced no row; this defends
//     against a previous lazy populate leaving a stale active view.
//   - RevokeForSubject: delegated to inner with NO cache operation. The only
//     caller is credentialinvalidate.Apply (archtest
//     CREDENTIAL-INVALIDATE-FUNNEL-01) which co-tx bumps users.authz_epoch.
//     Any stale cached ValidateView is rejected by sessionvalidate's epoch
//     invariant (user.AuthzEpoch != view.AuthzEpochAtIssue → fail-closed
//     401). user.AuthzEpoch is intentionally NOT cached — see backlog
//     AUTH-CACHE-SUBJECT-REVERSE-INDEX-01 for the upgrade path required
//     before that invariant relaxes.
//   - RepoReady: delegated to inner. Redis liveness is independently surfaced
//     by adapters/redis Client.Checkers (probe redis_ready).
//
// All cache.{Get,Set,Delete} errors are fail-safe: the wrapper logs at Warn
// and falls through to / continues with the inner result. Only inner errors
// propagate to callers, preserving sessionvalidate's KindUnavailable → 503
// semantics for genuine session-store outages.
//
// ref: alexedwards/scs redisstore/redisstore.go@master (PEXPIREAT object-level
// expiry alignment — we use fixed Duration TTL because ValidateView hides
// ExpiresAt by design, see runtime/auth/session.Session.ExpiresAt godoc).
// ref: go-redis/cache cache.go@v9 (fail-open + delete-on-write model).
type CachingSessionStore struct {
	inner  session.Store
	cache  *Cache
	ttl    time.Duration
	logger *slog.Logger
}

// sessionCacheKey is the per-id key prefix written under the Cache's
// KeyNamespace. Final Redis key = "<namespace>:session:<sessionID>".
const sessionCacheKey = "session:"

// NewCachingSessionStore constructs a CachingSessionStore. All three core
// dependencies are mandatory; nil inner / nil cache / non-positive ttl fail
// fast with errcode.ErrValidationFailed. Wiring layer is responsible for the
// enable/disable decision (env GOCELL_SESSION_CACHE_TTL = "" → do not call
// this constructor; ttl ≤ 0 → also do not call it). logger may be nil; the
// default slog logger is used in that case.
func NewCachingSessionStore(inner session.Store, cache *Cache, ttl time.Duration, logger *slog.Logger) (*CachingSessionStore, error) {
	if inner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"redis: NewCachingSessionStore requires non-nil inner session.Store")
	}
	if cache == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"redis: NewCachingSessionStore requires non-nil *Cache")
	}
	if ttl <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"redis: NewCachingSessionStore requires positive ttl")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CachingSessionStore{
		inner:  inner,
		cache:  cache,
		ttl:    ttl,
		logger: logger,
	}, nil
}

// Create delegates to inner. No cache write — see godoc rationale.
func (s *CachingSessionStore) Create(ctx context.Context, sess *session.Session) error {
	return s.inner.Create(ctx, sess)
}

// Get is the read-through hot path. Cache hit on a well-formed JSON entry
// returns immediately; any error in the cache path is logged at Warn and
// fallthrough occurs. On inner success the returned view is lazily populated
// into the cache for the next request.
func (s *CachingSessionStore) Get(ctx context.Context, id string) (*session.ValidateView, error) {
	key := sessionCacheKey + id

	if raw, err := s.cache.Get(ctx, key); err == nil && raw != "" {
		var view session.ValidateView
		if jerr := json.Unmarshal([]byte(raw), &view); jerr == nil {
			return &view, nil
		} else {
			s.logger.Warn("session-cache: corrupt cached entry; falling through",
				slog.String("sid", id),
				slog.Any("error", jerr))
		}
	} else if err != nil {
		s.logger.Warn("session-cache: get failed; falling through",
			slog.String("sid", id),
			slog.Any("error", err))
	}

	view, err := s.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if view != nil {
		s.lazyPopulate(ctx, key, view, id)
	}
	return view, nil
}

// lazyPopulate writes the freshly-fetched view back into Redis. Marshal or
// Set errors are logged at Warn and ignored — the caller already has the
// correct view in hand, the cache is best-effort.
func (s *CachingSessionStore) lazyPopulate(ctx context.Context, key string, view *session.ValidateView, sid string) {
	payload, err := json.Marshal(view)
	if err != nil {
		s.logger.Warn("session-cache: marshal failed; skipping populate",
			slog.String("sid", sid),
			slog.Any("error", err))
		return
	}
	if err := s.cache.Set(ctx, key, string(payload), s.ttl); err != nil {
		s.logger.Warn("session-cache: set failed; skipping populate",
			slog.String("sid", sid),
			slog.Any("error", err))
	}
}

// Revoke invokes inner.Revoke and then unconditionally deletes the cache
// entry. The Delete runs even when inner returned an error (e.g. infra
// failure) so a previously cached active view does not linger; Delete errors
// are swallowed via slog.Warn. If inner returned an error, that error
// propagates so callers (sessionvalidate, sessionlogout) preserve their
// existing semantics.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	innerErr := s.inner.Revoke(ctx, id)
	if delErr := s.cache.Delete(ctx, sessionCacheKey+id); delErr != nil && !errors.Is(delErr, context.Canceled) {
		s.logger.Warn("session-cache: delete after revoke failed",
			slog.String("sid", id),
			slog.Any("error", delErr))
	}
	return innerErr
}

// RevokeForSubject delegates to inner. Cache invalidation is intentionally
// omitted — see the type godoc and backlog AUTH-CACHE-SUBJECT-REVERSE-INDEX-01.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}

// RepoReady delegates to inner. Redis liveness is reported independently by
// the adapter-level redis_ready probe; a cached store does not need its own
// probe — cache outage is fail-safe (falls through to inner).
func (s *CachingSessionStore) RepoReady(ctx context.Context) error {
	return s.inner.RepoReady(ctx)
}

// Compile-time assertion that CachingSessionStore implements session.Store.
var _ session.Store = (*CachingSessionStore)(nil)
