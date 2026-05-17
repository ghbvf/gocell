package redis

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CachingSessionStore is a read-through Redis cache decorator over a
// runtime/auth/session.Store. The wrapped (inner) store remains the system of
// record; the Redis cache only accelerates Get. Construction-time fail-fast
// ensures wiring misconfigurations surface at startup, not at the first request.
//
// Behavior summary (T5/AUTH-CACHE-01 plan):
//
//   - Get: cache hit → unmarshal sessionCacheEntry → entry.validate(id). On
//     validation failure: slog.Warn + synchronous cache.Delete (best-effort,
//     Delete errors logged at Warn) + fall through to inner.Get. Miss / Redis
//     error / corrupt JSON → slog.Warn + synchronous cache.Delete (same
//     best-effort contract) + fall through to inner.Get. On inner success only
//     active (RevokedAt == nil) views are lazily populated into the cache
//     (Set error is also swallowed).
//   - Create: delegated to inner. No cache write — the first Get after Create
//     primes the cache via the read-through path (avoids "created then revoked"
//     edge thinking and removes a method's worth of code).
//   - Revoke: delegates to inner only. Cache invalidation is intentionally
//     omitted — stale-cache window is bounded by the cache TTL (≤
//     GOCELL_SESSION_CACHE_TTL, max 30s enforced at wiring). The in-transaction
//     cache.Delete pattern was removed in the third-round review because it
//     raced with concurrent re-population from the still-uncommitted PG row,
//     potentially extending the stale window to 2×TTL. See backlog
//     AUTH-CACHE-AFTER-COMMIT-INVALIDATION-01 for the kernel AfterCommit
//     primitive upgrade path required to reach 0-staleness.
//     Hard-locked by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
//   - RevokeForSubject: delegated to inner with NO cache operation. The only
//     caller is credentialinvalidate.Apply (archtest
//     CREDENTIAL-INVALIDATE-FUNNEL-01) which co-tx bumps users.authz_epoch.
//     Any stale cached ValidateView is rejected by sessionvalidate's epoch
//     invariant (user.AuthzEpoch != view.AuthzEpochAtIssue → fail-closed
//     401). user.AuthzEpoch is intentionally NOT cached — see backlog
//     AUTH-CACHE-SUBJECT-REVERSE-INDEX-01 for the upgrade path required
//     before that invariant relaxes.
//     Hard-locked by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
//   - RepoReady: delegated to inner. Redis liveness is independently surfaced
//     by adapters/redis Client.Checkers (probe redis_ready).
//
// All cache.{Get,Set,Delete} read-path errors are fail-safe: the wrapper logs
// at Warn and falls through to / continues with the inner result. Only inner
// errors propagate to callers, preserving sessionvalidate's KindUnavailable →
// 503 semantics for genuine session-store outages.
//
// # Threat model
//
//   - Stale-cache revoke window: by design, single-session sessionlogout's
//     revocation propagates to cache state only after the cache entry expires
//     (up to GOCELL_SESSION_CACHE_TTL, ≤30s by wiring). The upper bound is
//     one full TTL, NOT the remaining TTL at Revoke time: a Get that arrives
//     during the brief window between inner Revoke fetching pre-revoke state
//     and the row commit becoming visible causes lazyPopulate to write a
//     fresh full-TTL entry, resetting the clock. The in-transaction
//     cache.Delete pattern was removed in the third-round review (PR #524 →
//     fix PR) because it raced with concurrent re-population from the
//     still-uncommitted PG row, potentially extending the stale window to
//     2×TTL. Single-session sessionlogout does NOT bump user.AuthzEpoch, so
//     the epoch invariant in sessionvalidate.go does not catch this case —
//     the TTL is the security floor here. Mitigation: keep TTL short
//     (≤ GOCELL_SESSION_CACHE_TTL, ≤30s enforced at wiring by
//     wrapSessionStoreWithCache). RevokeForSubject paths (driven by
//     credentialinvalidate.Apply) have an independent security floor — the
//     co-tx user.AuthzEpoch bump fails the cached AuthzEpochAtIssue check in
//     sessionvalidate.go regardless of cache state, so the TTL bound above
//     does NOT apply to that path.
//   - Redis keyspace enumeration: cache keys take the form
//     accesscore:session:<rawSessionID>; anyone with redis-cli KEYS / SCAN
//     access can enumerate active session identifiers. Operators MUST gate
//     Redis with ACL so only ops accounts can enumerate the keyspace; the
//     cache itself does not hash the session ID (consistent with PG which
//     also stores raw sessions.id).
//   - JSON wire schema: a dedicated sessionCacheEntry struct (not the full
//     session.ValidateView) is the on-wire shape. Adding a sensitive field
//     to ValidateView does NOT automatically propagate into Redis — the
//     copy is explicit, providing an audit gate.
//
// # Ops guidance
//
// For high-security scenarios requiring instant logout effect (e.g. session
// compromise response), DO NOT enable this cache (leave GOCELL_SESSION_CACHE_TTL
// empty) or configure an extremely short TTL (e.g. 5s). The stale-cache window
// after a single-session Revoke is bounded by one full cache TTL (lazyPopulate
// can refresh the entry just before Revoke commits — see §Threat model) — with
// a 5s TTL the maximum exposure is 5s. See backlog
// AUTH-CACHE-AFTER-COMMIT-INVALIDATION-01 for the sub-TTL invalidation upgrade
// path (kernel AfterCommit primitive) that would reduce this window to near-zero
// without the 2×TTL race described in the Stale-cache revoke window threat above.
//
// ref: alexedwards/scs redisstore/redisstore.go@master (PEXPIREAT object-level
// expiry alignment — we use fixed Duration TTL because ValidateView hides
// ExpiresAt by design, see runtime/auth/session.Session.ExpiresAt godoc).
// ref: go-redis/cache cache.go@v9 (fail-open model).
type CachingSessionStore struct {
	inner  session.Store
	cache  *Cache
	ttl    time.Duration
	logger *slog.Logger
}

// sessionCacheKey is the per-id key prefix written under the Cache's
// KeyNamespace. Final Redis key = "<namespace>:session:<sessionID>".
const sessionCacheKey = "session:"

// sessionCacheEntry is the on-wire JSON shape persisted in Redis. It mirrors
// the four fields of session.ValidateView verbatim; using a dedicated struct
// makes field addition an explicit code change rather than an automatic
// propagation from session.ValidateView. Adding a sensitive field to
// ValidateView must be a deliberate decision to also land here.
type sessionCacheEntry struct {
	ID                string     `json:"id"`
	SubjectID         string     `json:"subjectId"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	AuthzEpochAtIssue int64      `json:"authzEpochAtIssue"`
}

func entryFromView(v *session.ValidateView) sessionCacheEntry {
	return sessionCacheEntry{
		ID:                v.ID,
		SubjectID:         v.SubjectID,
		RevokedAt:         v.RevokedAt,
		AuthzEpochAtIssue: v.AuthzEpochAtIssue,
	}
}

func (e sessionCacheEntry) toView() *session.ValidateView {
	return &session.ValidateView{
		ID:                e.ID,
		SubjectID:         e.SubjectID,
		RevokedAt:         e.RevokedAt,
		AuthzEpochAtIssue: e.AuthzEpochAtIssue,
	}
}

// validate enforces the wire-schema invariants the producer (lazyPopulate)
// upholds: ID must match the requested id, SubjectID must be non-empty,
// AuthzEpochAtIssue must be positive, and RevokedAt must be nil (only active
// views are written to cache). Failure → fall through to inner.
func (e sessionCacheEntry) validate(wantID string) error {
	if e.ID != wantID {
		return errors.New("session-cache: id mismatch")
	}
	if e.SubjectID == "" {
		return errors.New("session-cache: empty SubjectID")
	}
	if e.AuthzEpochAtIssue <= 0 {
		return errors.New("session-cache: non-positive AuthzEpochAtIssue")
	}
	if e.RevokedAt != nil {
		return errors.New("session-cache: cached revoked session")
	}
	return nil
}

// NewCachingSessionStore constructs a CachingSessionStore. All three core
// dependencies are mandatory; nil inner / nil cache / non-positive ttl fail
// fast with errcode.ErrValidationFailed. Wiring layer is responsible for the
// enable/disable decision (env GOCELL_SESSION_CACHE_TTL = "" → do not call
// this constructor; ttl ≤ 0 → also do not call it). logger may be nil; the
// default slog logger is used in that case.
func NewCachingSessionStore(inner session.Store, cache *Cache, ttl time.Duration, logger *slog.Logger) (*CachingSessionStore, error) {
	if validation.IsNilInterface(inner) {
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
//
// Both fall-through paths — corrupt JSON (unmarshal-fail) and invalid entry
// (validate-fail) — synchronously delete the bad cache key before falling
// through. This prevents repeated hits on a known-bad entry within the same
// TTL window. Delete errors are logged at Warn and ignored (best-effort).
func (s *CachingSessionStore) Get(ctx context.Context, id string) (*session.ValidateView, error) {
	key := sessionCacheKey + id
	if view := s.readCacheEntry(ctx, key, id); view != nil {
		return view, nil
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

// readCacheEntry attempts the cache read. Returns the cached ValidateView on
// a clean hit, nil on miss / cache error / corrupt entry / invalid entry.
// Corrupt and invalid entries are synchronously DEL'd before returning nil
// so the next Get within the same TTL window does not re-hit the same bad
// key. All cache-side errors are fail-safe — they only ever degrade to a
// cache miss, never propagate to the caller.
func (s *CachingSessionStore) readCacheEntry(ctx context.Context, key, id string) *session.ValidateView {
	raw, err := s.cache.Get(ctx, key)
	if err != nil {
		s.logger.Warn("session-cache: get failed; falling through",
			slog.String("sid", id),
			slog.Any("error", err))
		return nil
	}
	if raw == "" {
		return nil
	}
	var entry sessionCacheEntry
	if jerr := json.Unmarshal([]byte(raw), &entry); jerr != nil {
		s.evictBadEntry(ctx, key, id, "corrupt", jerr)
		return nil
	}
	if verr := entry.validate(id); verr != nil {
		s.evictBadEntry(ctx, key, id, "invalid", verr)
		return nil
	}
	return entry.toView()
}

// evictBadEntry logs a fall-through warning for a corrupt or invalid cache
// entry, then synchronously DELs the key. Both the log and the DEL are
// best-effort; DEL errors are logged at Warn and otherwise ignored.
func (s *CachingSessionStore) evictBadEntry(ctx context.Context, key, id, kind string, cause error) {
	s.logger.Warn("session-cache: bad cached entry; falling through",
		slog.String("sid", id),
		slog.String("kind", kind),
		slog.Any("error", cause))
	if delErr := s.cache.Delete(ctx, key); delErr != nil {
		s.logger.Warn("session-cache: failed to clean bad cached entry",
			slog.String("sid", id),
			slog.String("kind", kind),
			slog.Any("error", delErr))
	}
}

// lazyPopulate writes the freshly-fetched view back into Redis. Marshal or
// Set errors are logged at Warn and ignored — the caller already has the
// correct view in hand, the cache is best-effort. Revoked views are never
// written — only active (RevokedAt == nil) views go into the cache.
func (s *CachingSessionStore) lazyPopulate(ctx context.Context, key string, view *session.ValidateView, sid string) {
	if view.RevokedAt != nil {
		return // never cache revoked views — only active views go into the cache
	}
	payload, err := json.Marshal(entryFromView(view))
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

// Revoke delegates to inner. Cache invalidation is intentionally omitted —
// stale-cache window is bounded by one full cache TTL (≤ GOCELL_SESSION_CACHE_TTL,
// max 30s); see type godoc §Threat model for why the bound is full-TTL rather
// than remaining-TTL at Revoke time. Single-session sessionlogout does NOT
// bump user.AuthzEpoch, so the TTL is the security floor for this path —
// distinct from RevokeForSubject, where the epoch bump catches stale views
// regardless of cache state. The in-transaction cache.Delete pattern was
// rejected in the third-round review because it races with concurrent
// re-population from the still-uncommitted PG row, potentially extending
// the stale window to 2×TTL.
// See backlog AUTH-CACHE-AFTER-COMMIT-INVALIDATION-01 for the kernel
// AfterCommit primitive upgrade path required to reach 0-staleness.
//
// Hard-locked by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: this
// method body MUST be exactly one ReturnStmt delegating to inner with the
// same name.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject delegates to inner. Cache invalidation is omitted by
// design — the cached ValidateView's AuthzEpochAtIssue is compared by
// sessionvalidate.go against the live user.AuthzEpoch (bumped co-tx by
// credentialinvalidate.Apply); mismatch → 401 fail-closed regardless of
// cache state. user.AuthzEpoch is intentionally NOT cached — see backlog
// AUTH-CACHE-SUBJECT-REVERSE-INDEX-01 for the upgrade path required before
// that invariant relaxes.
//
// Hard-locked by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: this
// method body MUST be exactly one ReturnStmt delegating to inner with the
// same name.
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
