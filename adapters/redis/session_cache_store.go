package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/ctxutil"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
)

// CachingSessionStore is a read-through Redis cache decorator over a
// runtime/auth/session.Store. The wrapped (inner) store remains the system of
// record; the Redis cache only accelerates Get. Construction-time fail-fast
// ensures wiring misconfigurations surface at startup, not at the first request.
//
// Behavior summary (T5/AUTH-CACHE-01 plan; #794 metrics; #796 after-commit DEL):
//
//   - Get: cache hit → unmarshal sessionCacheEntry → entry.validate(id). On
//     validation failure: slog.Warn + synchronous cache.Delete (best-effort,
//     Delete errors logged at Warn) + fall through to inner.Get. Miss / Redis
//     error / corrupt JSON → slog.Warn + synchronous cache.Delete (same
//     best-effort contract) + fall through to inner.Get. On inner success only
//     active (RevokedAt == nil) views are lazily populated into the cache
//     (Set error is also swallowed). Every Get records exactly one hit or miss
//     metric; every cache-access error additionally records an error metric.
//   - Create: delegated to inner. No cache write — the first Get after Create
//     primes the cache via the read-through path (avoids "created then revoked"
//     edge thinking and removes a method's worth of code).
//   - Revoke: delegates to inner, then registers a post-commit cache-DEL via
//     persistence.RegisterAfterCommit (#796). The DEL fires AFTER the revoke
//     commit is durable, so it cannot race with concurrent re-population from a
//     still-uncommitted PG row (the 2×TTL race that the rejected in-transaction
//     cache.Delete suffered). This drives the single-session stale-cache window
//     to near-zero (the commit→hook gap) rather than the prior full-TTL floor.
//     Contract: Revoke MUST be called within a RunInTx scope (sessionlogout
//     wraps it — see §Threat model); calling it outside an active tx panics via
//     RegisterAfterCommit (programmer error). The hook is a pure transient side
//     effect (cache.Delete only) — archtest AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
//     forbids it from touching the tx/outbox, and archtest
//     CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 forbids any cache mutation in
//     the Revoke body OUTSIDE the after-commit hook (relocating, not removing,
//     the 2×TTL protection).
//   - RevokeForSubject: delegated to inner with NO cache operation. The only
//     caller is credentialinvalidate.Apply (archtest
//     CREDENTIAL-INVALIDATE-FUNNEL-01) which co-tx bumps users.authz_epoch.
//     Any stale cached ValidateView is rejected by sessionvalidate's epoch
//     invariant (user.AuthzEpoch != view.AuthzEpochAtIssue → fail-closed
//     401). user.AuthzEpoch is intentionally NOT cached, so this path needs no
//     after-commit DEL — the epoch bump is its near-zero invalidation already.
//     Hard-locked delegate-only by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
//     (Subject-wide cache purge for the future case where user state IS cached
//     is the deferred AUTH-CACHE-SUBJECT-REVERSE-INDEX-01 / gh #793.)
//   - RepoReady: delegated to inner. Redis liveness is independently surfaced
//     by adapters/redis Client.Checkers (probe redis_ready).
//
// All cache.{Get,Set,Delete} read-path errors are fail-safe: the wrapper logs
// at Warn (and records a session_cache_errors_total metric) and falls through
// to / continues with the inner result. Only inner errors propagate to callers,
// preserving sessionvalidate's KindUnavailable → 503 semantics for genuine
// session-store outages.
//
// # Threat model
//
//   - Stale-cache revoke window (single-session sessionlogout): Revoke now
//     registers an after-commit cache.Delete hook (#796), so the cached entry
//     is purged immediately after the revoke commit becomes durable. The
//     residual window is the commit→hook gap (near-zero — the hook runs
//     synchronously on the committing goroutine before RunInTx returns), NOT a
//     full TTL. The earlier in-transaction cache.Delete was rejected (PR #524 →
//     fix PR) because it raced with concurrent re-population from the
//     still-uncommitted PG row (a Get arriving between inner Revoke and commit
//     would lazyPopulate a fresh full-TTL entry), potentially extending the
//     window to 2×TTL; firing the DEL after commit removes that race because the
//     row is already revoked when the hook runs, so any concurrent lazyPopulate
//     reads the revoked state and skips the write (revoked views are never
//     cached). The TTL remains a fail-safe backstop: if the after-commit DEL
//     itself fails (best-effort, logged at Warn + counted as
//     session_cache_revoke_del_errors_total) the entry still expires at TTL —
//     a stale ≤ TTL window that is the accepted degraded behavior on this path
//     (single-session Revoke does not bump the epoch, so TTL is the only floor).
//     RevokeForSubject paths (credentialinvalidate.Apply) have an independent,
//     equally-near-zero floor — the co-tx user.AuthzEpoch bump fails the cached
//     AuthzEpochAtIssue check in sessionvalidate.go regardless of cache state.
//   - Redis keyspace enumeration: cache keys take the form
//     accesscore:session:<rawSessionID>; anyone with redis-cli KEYS / SCAN
//     access can enumerate active session identifiers. Operators MUST gate
//     Redis with ACL so only ops accounts can enumerate the keyspace; the
//     cache itself does not hash the session ID (consistent with PG which
//     also stores raw sessions.id).
//   - JSON wire schema: a dedicated sessionCacheEntry struct (not the full
//     session.ValidateView) is the on-wire shape. Adding a sensitive field
//     to ValidateView does NOT automatically propagate into Redis — the
//     copy is explicit, providing an audit gate. The flip side of that gate
//     (alexedwards/scs store contract: a cache projection must stay equivalent
//     to the source-of-record for every field the validate path consumes): an
//     authentication-DECISION field added to ValidateView MUST also land in
//     sessionCacheEntry, or a cache HIT silently degrades the decision. TenantID
//     (#1337 PR-3b: the RLS scope carrier) is such a field and is cached.
//
// # Ops guidance
//
// Single-session logout invalidates the cache near-instantly via the
// after-commit DEL (#796) — the residual stale window is only the
// commit→hook gap (effectively zero; the hook runs synchronously on the
// committing goroutine before RunInTx returns). Any concurrent Get that arrives
// between inner.Revoke and commit cannot re-populate the cache (the row is
// still uncommitted) and will fall through to inner; a Get after commit
// reads the revoked row and skips lazyPopulate.
//
// DEL-failure backstop is TTL, not epoch. Should the after-commit DEL itself
// fail (best-effort, logged at Warn + counted as
// session_cache_revoke_del_errors_total), the logged-out session's cached active
// ValidateView can still satisfy sessionvalidate (RevokedAt == nil, epoch
// unchanged) and be served on a cache HIT until the entry expires at TTL.
// Single-session Revoke does NOT bump users.authz_epoch, so the epoch-based
// fail-closed net does NOT apply on this path — that net is exclusive to
// RevokeForSubject / credential invalidation (which bumps the epoch co-tx, see
// §Threat model). The bounded residual (stale ≤ TTL, capped by
// GOCELL_SESSION_CACHE_TTL) is the accepted degraded behavior of #796's TTL
// backstop; operators alert on session_cache_revoke_del_errors_total to detect
// sustained DEL failures (docs/ops/alerting-rules.md). For zero-tolerance stale
// cache requirements (e.g. an ongoing breach investigation where every session
// must be invalidated atomically), disable the cache by leaving
// GOCELL_SESSION_CACHE_TTL empty — this removes the cache entirely from the
// trust path.
//
// ref: alexedwards/scs redisstore/redisstore.go@master (PEXPIREAT object-level
// expiry alignment — we use fixed Duration TTL because ValidateView hides
// ExpiresAt by design, see runtime/auth/session.Session.ExpiresAt godoc).
// ref: go-redis/cache cache.go@v9 (fail-open model).
// ref: spring-tx TransactionSynchronization.afterCommit (post-commit cache
// eviction); kernel/persistence.RegisterAfterCommit is the GoCell analog.
type CachingSessionStore struct {
	inner   session.Store
	cache   *Cache
	ttl     time.Duration
	logger  *slog.Logger
	metrics cacheMetricsRecorder
}

// cacheMetricsRecorder is the hit/miss/error sink for the read-through cache.
// It is a local 3-method interface (not the concrete
// runtime/observability/metrics.SessionCacheCollector) so this hot-path file
// stays free of a runtime/observability import and the metric wiring is
// unit-testable with a spy. The concrete collector is constructed in the
// composition root (cellmodules/accesscore) and injected via
// NewCachingSessionStore; a nil recorder falls back to nopCacheMetrics.
type cacheMetricsRecorder interface {
	RecordHit(ctx context.Context)
	RecordMiss(ctx context.Context)
	RecordError(ctx context.Context)
	// RecordRevokeDelError records a post-commit revoke cache-DEL failure
	// (#796). It is a DISTINCT signal from RecordError: a read-path error is
	// always paired with a miss (errors ⊆ misses), whereas a revoke DEL
	// failure fires outside any Get and has no paired miss, so it must not
	// pollute the read-path errors_total series.
	RecordRevokeDelError(ctx context.Context)
}

// nopCacheMetrics is the disabled-metrics fallback. Metrics are an optional
// observability dependency, so a cache constructed without a collector (nil
// recorder) must still function — these no-ops carry that decision.
type nopCacheMetrics struct{}

func (nopCacheMetrics) RecordHit(context.Context) {
	// Metrics are optional; nil collectors intentionally record nothing.
}

func (nopCacheMetrics) RecordMiss(context.Context) {
	// Metrics are optional; nil collectors intentionally record nothing.
}

func (nopCacheMetrics) RecordError(context.Context) {
	// Metrics are optional; nil collectors intentionally record nothing.
}

func (nopCacheMetrics) RecordRevokeDelError(context.Context) {
	// Metrics are optional; nil collectors intentionally record nothing.
}

// sessionCacheKey is the per-id key prefix written under the Cache's
// KeyNamespace. Final Redis key = "<namespace>:session:<sessionID>".
const sessionCacheKey = "session:"

// sessionCacheLogPrefix is the slog / errors message prefix for all
// session-cache log lines. Kept separate from sessionCacheKey (Redis key
// prefix) — the two constants serve orthogonal purposes.
const sessionCacheLogPrefix = "session-cache: "

// sessionCacheRevokeDELTimeout is the per-call deadline for the post-commit
// cache.Delete issued by Revoke's after-commit hook. DEL is best-effort; this
// cap prevents a slow or unavailable Redis from blocking the committing
// goroutine / RunInTx return for an unbounded time.
// Aligns with bootstrapAppendDetachedTimeout = 2s (pkg/ctxutil).
const sessionCacheRevokeDELTimeout = 2 * time.Second

// sessionCacheEntry is the on-wire JSON shape persisted in Redis. It mirrors
// the five fields of session.ValidateView verbatim; using a dedicated struct
// makes field addition an explicit code change rather than an automatic
// propagation from session.ValidateView. Adding a sensitive field to
// ValidateView must be a deliberate decision to also land here.
//
// TenantID is a first-class authentication-decision carrier (#1337 PR-3b):
// sessionvalidate/sessionrefresh derive the RLS tenant scope from view.TenantID,
// so a cache entry that dropped it would, on a cache HIT, hand the consumer a
// zero-value tenant and break the scoped read (tenant.TenantID.Validate fails or
// the scope is wrong). It is therefore part of the cached projection, not an
// optional extra — see the SCS store-contract note in the type godoc above.
//
// The field set is frozen by INVARIANT SESSION-CACHE-EPOCH-NOT-CACHED-01
// (session_cache_entry_frozen_test.go): it must NOT carry the live
// users.authz_epoch — only AuthzEpochAtIssue (the at-issue snapshot). The
// RevokeForSubject fail-closed floor depends on the live epoch never being
// cached (sessionvalidate compares the live PG epoch against this snapshot); a
// reflect freeze prevents a future field addition from silently removing that
// premise.
type sessionCacheEntry struct {
	ID                string          `json:"id"`
	SubjectID         string          `json:"subjectId"`
	TenantID          tenant.TenantID `json:"tenantId"`
	RevokedAt         *time.Time      `json:"revokedAt,omitempty"`
	AuthzEpochAtIssue int64           `json:"authzEpochAtIssue"`
}

func entryFromView(v *session.ValidateView) sessionCacheEntry {
	return sessionCacheEntry{
		ID:                v.ID,
		SubjectID:         v.SubjectID,
		TenantID:          v.TenantID,
		RevokedAt:         v.RevokedAt,
		AuthzEpochAtIssue: v.AuthzEpochAtIssue,
	}
}

func (e sessionCacheEntry) toView() *session.ValidateView {
	return &session.ValidateView{
		ID:                e.ID,
		SubjectID:         e.SubjectID,
		TenantID:          e.TenantID,
		RevokedAt:         e.RevokedAt,
		AuthzEpochAtIssue: e.AuthzEpochAtIssue,
	}
}

// validate enforces the wire-schema invariants the producer (lazyPopulate)
// upholds: ID must match the requested id, SubjectID must be non-empty,
// TenantID must be a valid (non-empty, canonical-UUID) tenant, AuthzEpochAtIssue
// must be positive, and RevokedAt must be nil (only active views are written to
// cache). Failure → fall through to inner.
//
// The TenantID check doubles as the migration backstop: a pre-PR-3b cache entry
// (written without tenantId) deserializes to the empty TenantID, fails
// Validate, and falls through to the inner store — which returns the row with
// the correct tenant and re-populates the cache. No stale tenant-less entry can
// be served as a cache hit.
func (e sessionCacheEntry) validate(wantID string) error {
	if e.ID != wantID {
		return errors.New(sessionCacheLogPrefix + "id mismatch")
	}
	if e.SubjectID == "" {
		return errors.New(sessionCacheLogPrefix + "empty SubjectID")
	}
	if err := e.TenantID.Validate(); err != nil {
		return fmt.Errorf(sessionCacheLogPrefix+"invalid TenantID: %w", err)
	}
	if e.AuthzEpochAtIssue <= 0 {
		return errors.New(sessionCacheLogPrefix + "non-positive AuthzEpochAtIssue")
	}
	if e.RevokedAt != nil {
		return errors.New(sessionCacheLogPrefix + "cached revoked session")
	}
	return nil
}

// NewCachingSessionStore constructs a CachingSessionStore. The three core
// dependencies are mandatory; nil inner / nil cache / non-positive ttl fail
// fast with errcode.ErrValidationFailed. Wiring layer is responsible for the
// enable/disable decision (env GOCELL_SESSION_CACHE_TTL = "" → do not call
// this constructor; ttl ≤ 0 → also do not call it). logger may be nil; the
// default slog logger is used in that case. recorder is the optional
// hit/miss/error metrics sink (#794) — nil falls back to a no-op so metrics
// stay an optional observability dependency.
func NewCachingSessionStore(
	inner session.Store, cache *Cache, ttl time.Duration, logger *slog.Logger, recorder cacheMetricsRecorder,
) (*CachingSessionStore, error) {
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
	if validation.IsNilInterface(recorder) {
		recorder = nopCacheMetrics{}
	}
	return &CachingSessionStore{
		inner:   inner,
		cache:   cache,
		ttl:     ttl,
		logger:  logger,
		metrics: recorder,
	}, nil
}

// Create delegates to inner. No cache write — see godoc rationale.
func (s *CachingSessionStore) Create(ctx context.Context, t tenant.TenantID, sess *session.Session) error {
	return s.inner.Create(ctx, t, sess)
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
		s.metrics.RecordHit(ctx)
		return view, nil
	}
	// Every non-hit (empty, Redis error, corrupt, invalid) is a miss for
	// hit-rate purposes; readCacheEntry has already recorded a read-path error
	// metric for the error sub-cases (read-path errors ⊆ misses; the revoke DEL
	// failure is a separate series — see RecordRevokeDelError).
	s.metrics.RecordMiss(ctx)
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
		s.metrics.RecordError(ctx)
		s.logger.Warn(sessionCacheLogPrefix+"get failed; falling through",
			slog.String("session_id", id),
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
	// A corrupt or schema-invalid cached entry is a cache-access error (the
	// secondary Delete failure below is not separately counted — the bad entry
	// is the meaningful event).
	s.metrics.RecordError(ctx)
	s.logger.Warn(sessionCacheLogPrefix+"bad cached entry; falling through",
		slog.String("session_id", id),
		slog.String("kind", kind),
		slog.Any("error", cause))
	if delErr := s.cache.Delete(ctx, key); delErr != nil {
		s.logger.Warn(sessionCacheLogPrefix+"failed to clean bad cached entry",
			slog.String("session_id", id),
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
		s.metrics.RecordError(ctx)
		s.logger.Warn(sessionCacheLogPrefix+"marshal failed; skipping populate",
			slog.String("session_id", sid),
			slog.Any("error", err))
		return
	}
	if err := s.cache.Set(ctx, key, string(payload), s.ttl); err != nil {
		s.metrics.RecordError(ctx)
		s.logger.Warn(sessionCacheLogPrefix+"set failed; skipping populate",
			slog.String("session_id", sid),
			slog.Any("error", err))
	}
}

// Revoke delegates to inner, then schedules a post-commit cache eviction so the
// stale-cache window after a single-session logout is near-zero (#796).
//
// The cache.Delete is registered via persistence.RegisterAfterCommit, so it
// fires AFTER the enclosing transaction's commit is durable — never inside it.
// This is the crux of the fix: the previously-rejected in-transaction
// cache.Delete raced with concurrent re-population from the still-uncommitted PG
// row (a Get between inner Revoke and commit would lazyPopulate a fresh
// full-TTL entry, extending the window to 2×TTL). Firing after commit removes
// the race — the row is already revoked when the hook runs, so any racing
// lazyPopulate reads the revoked state and skips the write (revoked views are
// never cached). The DEL is best-effort: a failure is logged at Warn and the
// entry then expires at TTL (the TTL is the fail-safe backstop). DEL is wrapped
// in ctxutil.WithDetachedTimeout(2s) so a slow Redis cannot block the
// committing goroutine / RunInTx return.
//
// Contract (decorator-specific LSP narrowing): Revoke MUST be invoked within a
// RunInTx scope (sessionlogout's persistRevoke wraps it). This is the canonical
// narrowing the general session.Store.Revoke contract (runtime/auth/session.Store)
// now documents as permitted for decorators — the bare PG / mem store has no
// tx-only precondition. RegisterAfterCommit panics if
// no ambient after-commit registry is present: calling Revoke outside a
// transaction is a programmer error, surfaced loudly rather than silently
// dropping the eviction. The shared storetest conformance suite, which calls
// Revoke bare, supplies the unit-of-work scope via the test-only
// txScopedRevokeStore bridge (session_cache_store_conformance_test.go).
// Compile-enforcing this precondition (a typed tx-scoped revoke capability
// rather than a runtime panic) was evaluated as gh #1615's F3 and resolved
// won't-do: a same-package seal is unreachable because the read path's
// evictBadEntry / lazyPopulate legitimately need a synchronous s.cache.Delete /
// s.cache.Set, so the cache-mutation capability must live on a struct field that
// every method (Revoke included) can reach — Go has no per-method field scoping.
// A cross-package internal-subpackage seal could reach Hard but is
// disproportionate for this single-caller P3 and has zero industry precedent
// (Spring TransactionSynchronization, Hibernate AfterTransactionCompletionProcess,
// ent CommitHook, Watermill forwarder all rely on runtime registration +
// convention, never a type seal). The "second production caller" risk is instead
// caught statically by archtest SESSION-REVOKE-CALLER-INTX-01, which allowlists
// the production callers of session.Store.Revoke (each verified RunInTx-wrapped).
//
// archtest CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 locks the shape: the body
// must delegate to s.inner.Revoke, and any s.cache.Delete/Set must be lexically
// inside the RegisterAfterCommit hook literal (never in the tx body — that is
// the relocated 2×TTL protection). archtest AFTERCOMMIT-HOOK-PURE-TRANSIENT-01
// independently forbids the hook from touching the tx or an outbox.Writer.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	if err := s.inner.Revoke(ctx, id); err != nil {
		return err
	}
	key := sessionCacheKey + id
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		hookCtx, cancel := ctxutil.WithDetachedTimeout(hookCtx, sessionCacheRevokeDELTimeout)
		defer cancel()
		if delErr := s.cache.Delete(hookCtx, key); delErr != nil {
			// Distinct from the read-path RecordError: a revoke DEL failure has
			// no paired RecordMiss (it fires outside any Get), so counting it
			// here keeps the read-path errors ⊆ misses contract intact while
			// still surfacing the security-relevant degraded invalidation.
			s.metrics.RecordRevokeDelError(hookCtx)
			s.logger.Warn(sessionCacheLogPrefix+"post-commit revoke DEL failed; entry expires at TTL",
				slog.String("session_id", id),
				slog.Any("error", delErr))
		}
	})
	return nil
}

// RevokeForSubject delegates to inner. Cache invalidation is omitted by
// design — the cached ValidateView's AuthzEpochAtIssue is compared by
// sessionvalidate.go against the live user.AuthzEpoch (bumped co-tx by
// credentialinvalidate.Apply); mismatch → 401 fail-closed regardless of
// cache state. user.AuthzEpoch is intentionally NOT cached.
//
// Hard-locked by archtest CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: this
// method body MUST be exactly one ReturnStmt delegating to inner with the
// same name.
func (s *CachingSessionStore) RevokeForSubject(
	ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken,
) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}

// RepoReady delegates to inner. Redis liveness is reported independently by
// the adapter-level redis_ready probe; a cached store does not need its own
// probe — cache outage is fail-safe (falls through to inner).
func (s *CachingSessionStore) RepoReady(ctx context.Context) error {
	return s.inner.RepoReady(ctx)
}

// Compile-time assertion that CachingSessionStore implements session.Store.
var _ session.Store = (*CachingSessionStore)(nil)
