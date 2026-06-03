// Package sessionrefresh implements the session-refresh slice: validates an
// opaque refresh token via refresh.Store and issues a fresh access JWT.
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// reuseCascadeTimeout bounds the detached invalidator.Apply transaction so
// a stalled DB cannot leak goroutines + pool connections indefinitely.
// Mirrors the cascade-revoke bound used by refresh.Store.RevokeSessionDetached
// (ADR 202605051800), keeping a single project-wide convention for
// security-cascade writes.
const reuseCascadeTimeout = 5 * time.Second

const errMsgInvalidRefreshToken = "invalid refresh token"

// Option configures a session-refresh Service.
type Option func(*Service)

// WithTxManager wires the cross-store CellTxManager. The Refresh flow wraps
// the validate→update→rotate sequence in a single RunInTx so the session
// repo and refresh store updates share one commit boundary; nil tx is
// silently ignored to keep the option idempotent — final non-nil enforcement
// is in NewService. Callers obtain the sealed marker via
// persistence.WrapForCell from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// WithInvalidator injects the credential-invalidation funnel used to
// cascade epoch bump + session revoke + refresh chain revoke on refresh-token
// reuse detection. Required — NewService fails fast when nil.
// Nil is silently ignored to keep the option idempotent; final nil
// enforcement is in NewService.
//
// Type rationale: the parameter is credentialinvalidate.Applier (the exported
// funnel interface), not *credentialinvalidate.Invalidator. The interface
// must live in credentialinvalidate so the callsite-level archtest
// (CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01) can resolve s.invalidator.Apply
// via info.Selections to a *types.Func with Pkg=credentialinvalidate.
// Production wiring still passes a *Invalidator (which satisfies Applier);
// unit tests inject a spy.
func WithInvalidator(inv credentialinvalidate.Applier) Option {
	return func(s *Service) {
		if !validation.IsNilInterface(inv) {
			s.invalidator = inv
		}
	}
}

// Service implements token refresh logic.
type Service struct {
	sessionStore session.Store             `gocell:"required"`
	userRepo     ports.UserRepository      `gocell:"required"`
	roleRepo     ports.RoleRepository      `gocell:"required"`
	refreshStore refresh.Store             `gocell:"required"`
	txRunner     persistence.CellTxManager `gocell:"required" gocellErr:"sessionrefresh: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	// invalidator is the credential-revocation funnel. Required — NewService
	// fails fast when nil. On refresh-token reuse detection, Apply is called
	// inside the outer transaction to atomically bump authz_epoch, revoke all
	// sessions, and revoke all refresh chains for the subject.
	invalidator credentialinvalidate.Applier `gocell:"required" gocellErr:"sessionrefresh: Invalidator required; use WithInvalidator"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	issuer      *auth.JWTIssuer              `gocell:"required"`
	logger      *slog.Logger
	clock       clock.Clock
}

// NewServiceParams holds the required dependencies for NewService. Keeping the
// required-dependency set in a struct reduces the positional-parameter count
// and makes call sites self-documenting (S107).
type NewServiceParams struct {
	SessionStore session.Store
	RoleRepo     ports.RoleRepository
	UserRepo     ports.UserRepository
	RefreshStore refresh.Store
	Issuer       *auth.JWTIssuer
}

// NewService creates a session-refresh Service.
//
// userRepo is required (P1-3 fix): fetchPasswordResetRequired silently
// returns false when userRepo is nil, which bypasses the password-reset
// security gate.
//
// refreshStore owns both token-state validation and rotation — the slice
// no longer parses JWTs or performs application-layer reuse detection.
//
// opts allows future functional extensions without breaking callers (F8).
func NewService(
	clk clock.Clock,
	params NewServiceParams,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "sessionrefresh.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionStore: params.SessionStore,
		roleRepo:     params.RoleRepo,
		userRepo:     params.UserRepo,
		refreshStore: params.RefreshStore,
		clock:        clk,
		issuer:       params.Issuer,
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Refresh validates the presented opaque refresh token, checks the backing
// session and subject, mints a new access JWT, and rotates the refresh token.
// Token rejection surfaces ErrAuthRefreshFailed; dependency failures surface
// ErrAuthRefreshUnavailable so clients do not confuse an outage with invalid
// credentials.
//
// 检查顺序 (ADR §A11 重写 + §A12 wire-uniformity + §A13 single-envelope):
//
//  1. refreshStore.Peek + verifySession
//  2. **session-state inline check (RevokedAt)** — revoked → cascadeRevoke +
//     uniform 401 ErrAuthRefreshFailed; user is never loaded.
//  3. subject-mismatch check → cascadeRevoke + uniform 401
//  4. fetchUserForRefresh (user lookup)
//  5. rejectIfUserNotActive — uniform 401 ErrAuthRefreshFailed (ADR §A13
//     single-envelope: indistinguishable from revoked/stale/reuse to caller)
//  6. rejectIfStaleEpoch → cascadeRevoke + uniform 401
//  7. mint + rotate
//
// Presenting an access JWT (or any string that does not parse as the opaque
// selector.verifier wire format) fails ParseOpaque inside refresh.Store and
// returns refresh.ErrRejected — the same fail-closed behavior the access-token
// confusion defense relies on.
//
// Session lifecycle: refresh does NOT mutate session.Store. session.ID is
// stable from login to logout; the access JWT carries the same sid claim
// across rotations. AuthzEpoch staleness is enforced by sessionvalidate
// reading users.authz_epoch (S4b), not by session-row rotation. This aligns
// with OAuth2 RFC 6749 §6 (refresh = same authorization grant), OIDC
// Back-Channel Logout (sid stable across refresh), and the ory-fosite /
// zitadel / keycloak implementations.
//
// Transactional scope: the Peek → verifySession → Rotate sequence runs inside
// txRunner.RunInTx so refresh-store writes commit atomically with the
// caller-supplied transaction boundary. session.Store is read-only on the
// refresh path; cascade revokes go through refreshStore.RevokeSessionDetached,
// which intentionally bypasses the outer transaction (PR#395 detached-context
// invariant).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (dto.TokenPair, error) {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthRefreshInvalidInput,
		validation.F("refreshToken", refreshToken),
	); err != nil {
		return dto.TokenPair{}, err
	}

	// outerCtx is the caller's context, captured here so refreshInTx can pass
	// it to handleRotateError. On reuse detection, Apply must run in a detached
	// tx that is independent of the outer RunInTx boundary — otherwise the 401
	// return causes the outer tx to roll back, undoing the cascade writes.
	outerCtx := ctx

	var pair dto.TokenPair
	do := func(txCtx context.Context) error {
		var err error
		pair, err = s.refreshInTx(txCtx, outerCtx, refreshToken)
		return err
	}
	if err := s.txRunner.RunInTx(ctx, do); err != nil {
		return dto.TokenPair{}, err
	}

	s.logger.Info("token refreshed", slog.String("user_id", pair.UserID))
	return pair, nil
}

// refreshInTx executes the validate→mint→rotate sequence under the outer
// RunInTx boundary established by Refresh. With a real PG TxRunner
// (postgres.TxManager), refresh-store calls participate in the outer
// transaction via savepoint nesting and roll back together on abort; with
// a no-op TxRunner (outbox.DemoTxRunner) the closure executes directly without
// TX semantics. Cascade-revoke calls intentionally bypass the outer TX
// through RevokeSessionDetached (PR#395 detached-context invariant).
//
// outerCtx is the caller's context from Refresh (before RunInTx). It is
// passed to handleRotateError so that the reuse-cascade Apply call uses a
// detached tx independent of the outer RunInTx boundary (Finding #4).
//
// session.Store is read-only on this path: refresh keeps session.ID stable
// across rotations (OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid
// stability). AuthzEpoch staleness is detected via rejectIfStaleEpoch (S4d
// row-provenance: compares presented.AuthzEpochAtIssue to user.AuthzEpoch()).
// handlePeekError classifies a Peek error and produces the service-layer
// error: ErrReused routes into the unified reuse cascade entry
// (handleReuseDetected) using whatever row identity Peek conveyed; other
// errors go through refreshStoreError. Extracted from refreshInTx to keep
// refreshInTx within the cognitive-complexity budget (≤15) after S4d added
// the stale-epoch branch.
//
// Reuse detected on Peek (grace-counter cap or post-rotation reuse window):
// the refresh store has already revoked the *single* presented session via
// revokeSessionDetachedAt, but cross-session credential invalidation (all
// sessions for the subject + all refresh chains + authz_epoch bump) only
// runs through invalidator.Apply. Route Peek's reuse signal to the same
// cascade entry point used by Rotate so the security response is identical
// regardless of which validation stage detected the attack (Finding #2 /
// PR #490 review). Note: stale-epoch is NOT a reuse attack and routes through
// rejectIfStaleEpoch (session-scoped revoke only, no RefreshReuse event).
func (s *Service) handlePeekError(outerCtx context.Context, presented *refresh.Token, err error) error {
	if errors.Is(err, refresh.ErrReused) {
		return s.handleReuseDetected(outerCtx, presentedSubjectID(presented), presentedSessionID(presented), "peek")
	}
	return s.refreshStoreError("session-refresh: refresh store peek failed", err)
}

func (s *Service) refreshInTx(ctx context.Context, outerCtx context.Context, refreshToken string) (dto.TokenPair, error) {
	presented, err := s.refreshStore.Peek(ctx, refreshToken)
	if err != nil {
		return dto.TokenPair{}, s.handlePeekError(outerCtx, presented, err)
	}

	// Belt-and-braces: double-check the backing session has not been revoked
	// out-of-band (e.g. a logout that bypassed the refresh store).
	sess, err := s.verifySession(ctx, presented.SessionID)
	if err != nil {
		return dto.TokenPair{}, err
	}

	// Session-state inline check — must run BEFORE any user lookup so a
	// revoked session never escalates to 403 (user-not-active) or 503
	// (userRepo outage). Extracted to rejectIfRevokedSession to keep
	// refreshInTx cognitive complexity within the ≤15 budget.
	if err := s.rejectIfRevokedSession(ctx, sess, presented.SubjectID); err != nil {
		return dto.TokenPair{}, err
	}

	if sess.SubjectID != presented.SubjectID {
		s.cascadeRevoke(ctx, presented.SessionID, "subject-mismatch")
		return dto.TokenPair{}, authRefreshRejected()
	}

	user, err := s.fetchUserForRefresh(ctx, sess.ID, sess.SubjectID)
	if err != nil {
		return dto.TokenPair{}, err
	}
	// User-bound credentialauthority funnel (ADR §A11 重写后, user-bound
	// only). Session-revoked already rejected above. Baseline
	// (CanAuthenticate) failure surfaces as uniform 401 ErrAuthRefreshFailed
	// (ADR §A13 single-envelope). cascadeRevoke clears the refresh chain so
	// subsequent rotation attempts fail immediately.
	if err := s.rejectIfUserNotActive(ctx, user, sess.ID); err != nil {
		return dto.TokenPair{}, err
	}

	if err := s.rejectIfStaleEpoch(ctx, presented.AuthzEpochAtIssue, user.AuthzEpoch(), sess.ID, sess.SubjectID); err != nil {
		return dto.TokenPair{}, err
	}

	passwordResetRequired := user.PasswordResetRequired()

	// session.ID is stable across refresh — the access JWT carries the same
	// sid claim as the original login. AuthzEpoch / password-reset state is
	// re-evaluated per refresh via the user lookup above; the session row
	// itself is not rotated.
	//
	// Tenant derivation (#1337 PR-2 stopgap): the refresh endpoint is Public
	// (no JWT), so there is no pre-auth ctx tenant. Derive the tenant from the
	// user row returned by fetchUserForRefresh (GetByID by-PK carve-out).
	// user.TenantID was stamped at Create time and is the authoritative source.
	// PR-3 will carry tenant in the refresh token / session row for true RLS
	// isolation; at that point this derivation moves to the store layer.
	refreshTenantID := user.TenantID
	// Defense-in-depth: validate the derived tenant before using it (#1337
	// PR-2a review U5). user.TenantID is normally stamped at Create time
	// and should always be a canonical UUID; a missing or malformed value
	// here would indicate a data integrity issue in the user row. Fail
	// closed to authRefreshRejected (the same envelope as every other
	// refresh rejection) to avoid leaking information about the failure.
	if err := refreshTenantID.Validate(); err != nil {
		s.logger.Error("session-refresh: invalid tenant derived from user row (fail-closed)",
			slog.Any("error", err),
			slog.String("subject_id", sess.SubjectID))
		return dto.TokenPair{}, authRefreshRejected()
	}
	minted, err := sessionmint.MintAccess(ctx, s.clock, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
	}, sessionmint.Request{
		UserID:                sess.SubjectID,
		SessionID:             sess.ID,
		PasswordResetRequired: passwordResetRequired,
		TenantID:              refreshTenantID,
	})
	if err != nil {
		s.logger.Error("session-refresh: token issuance failed",
			slog.Any("error", err),
			slog.String("user_id", sess.SubjectID),
			slog.String("session_id", sess.ID))
		return dto.TokenPair{}, err
	}

	newWire, rotated, err := s.refreshStore.Rotate(ctx, refreshToken)
	if err != nil {
		return dto.TokenPair{}, s.handleRotateError(outerCtx, err, sess.SubjectID, sess.ID)
	}
	// rotated.SessionID must match the verified session; defend against
	// concurrent drift between Peek and Rotate.
	if rotated.SessionID != sess.ID || rotated.SubjectID != sess.SubjectID {
		s.cascadeRevoke(ctx, sess.ID, "rotated-subject-mismatch")
		return dto.TokenPair{}, authRefreshRejected()
	}

	return dto.TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          newWire,
		ExpiresAt:             minted.ExpiresAt,
		SessionID:             sess.ID,
		UserID:                sess.SubjectID,
		PasswordResetRequired: passwordResetRequired,
	}, nil
}

// handleRotateError interprets a Rotate error and returns the appropriate
// service-layer error. On ErrReused it triggers the invalidator cascade in a
// detached, time-bounded tx; on other errors it delegates to refreshStoreError.
//
// outerCtx is the caller's context from Refresh (captured before RunInTx).
// On reuse detection, Apply must run in a tx that is detached from the outer
// RunInTx boundary — the same pattern as RevokeSessionDetached (cascadeRevoke).
// Without detachment, the 401 return from this function causes the outer tx to
// roll back, undoing the epoch bump + session revoke + refresh chain revoke
// cascade writes (Finding #4 bug).
//
// ref: golang.org/pkg/context#WithoutCancel; hashicorp/vault token_store.go
// quitContext; ADR docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md.
func (s *Service) handleRotateError(outerCtx context.Context, rotateErr error, subjectID, sessionID string) error {
	if !errors.Is(rotateErr, refresh.ErrReused) {
		return s.refreshStoreError("session-refresh: refresh store rotate failed", rotateErr)
	}
	return s.handleReuseDetected(outerCtx, subjectID, sessionID, "rotate")
}

// handleReuseDetected is the single entry point for refresh-reuse credential
// invalidation. Both Peek (post-rotation reuse window / grace-cap exhaustion)
// and Rotate (consumed-token replay) route their reuse signal here so the
// security response — atomic authz_epoch bump + RevokeForSubject (all sessions)
// + refresh chain revoke — is identical regardless of which validation stage
// flagged the attack (Finding #2 PR #490 review).
//
// The cascade runs inside a detached, time-bounded context: outer cancellation
// must not roll back the security write (the caller will return 401, which
// would otherwise abort the outer RunInTx), and DB stalls must not leak
// goroutines or pool connections (Finding #7 PR #490 review).
//
// ref: ADR 202605051800-adr-refresh-store-ambient-tx-and-idle-grace §"cascade detachment"
// ref: keycloak TokenManager refresh path — reuse triggers full session revocation
// ref: ory/fosite handler/oauth2/flow_refresh.go — reuse cascade at the flow boundary
func (s *Service) handleReuseDetected(outerCtx context.Context, subjectID, sessionID, stage string) error {
	if subjectID == "" {
		// refresh.Store contract (godoc on the Store interface) mandates a
		// non-empty SubjectID alongside ErrReused so the service layer can
		// drive the user-wide invalidation cascade. Reaching this branch in
		// production means an upstream Store implementation violated the
		// contract — silently 401ing here would let cross-session cascade
		// regress unnoticed, exactly the trap that motivated this fix.
		// Panic via the registered marker so the runtime Recovery
		// middleware converts it to a 500 with a loud audit trail; the
		// runtime/auth/refresh/storetest conformance suite catches the
		// contract drift in CI before production sees it.
		panic(panicregister.Approved("sessionrefresh-reuse-empty-subject",
			errcode.Assertion("sessionrefresh.handleReuseDetected: refresh.Store violated contract — returned ErrReused with empty SubjectID")))
	}
	// Derive the tenant from the user row. The refresh endpoint is Public (no
	// JWT), so there is no pre-auth ctx tenant. GetByID is the by-PK
	// tenant-deriving carve-out (#1337 PR-2a); it returns the row regardless of
	// tenant and the TenantID stamped at Create time is the authoritative source.
	// On any error (user not found, infra outage) fail-closed to 401: the reuse
	// attack is confirmed regardless, and surfacing a different status code would
	// leak side-channel information.
	userForTenant, err := s.userRepo.GetByID(outerCtx, subjectID)
	if err != nil {
		s.logger.Error("session-refresh: reuse cascade: failed to fetch user for tenant derivation (fail-closed to 401)",
			slog.Any("error", err),
			slog.String("stage", stage),
			slog.String("subject_id", subjectID),
			slog.String("session_id", sessionID))
		return authRefreshRejected()
	}
	reuseTenantID := userForTenant.TenantID
	// Defense-in-depth: validate the derived tenant before using it (#1337
	// PR-2a review U5). Fail closed to authRefreshRejected — the reuse
	// attack is confirmed regardless of cascade health; surfacing a
	// different code would leak side-channel info.
	if err := reuseTenantID.Validate(); err != nil {
		s.logger.Error("session-refresh: reuse cascade: invalid tenant derived from user row (fail-closed)",
			slog.Any("error", err),
			slog.String("stage", stage),
			slog.String("subject_id", subjectID))
		return authRefreshRejected()
	}
	detachedCtx, cancel := ctxutil.WithDetachedTimeout(outerCtx, reuseCascadeTimeout)
	defer cancel()
	if applyErr := s.txRunner.RunInTx(detachedCtx, func(txCtx context.Context) error {
		return s.invalidator.Apply(txCtx, reuseTenantID, subjectID, session.CredentialEventRefreshReuse)
	}); applyErr != nil {
		// Reuse has already been identified as an attack — the wire response
		// must be uniform 401 regardless of whether the cascade infrastructure
		// (DB, dependent stores) is currently healthy. Surfacing applyErr here
		// would let an infra KindUnavailable bubble through the middleware as
		// 503, leaking a side-channel signal that "the cascade tried but
		// failed". Log the cascade failure for operator follow-up, then
		// fail-closed to the same uniform 401 rejection.
		s.logger.Error("session-refresh: reuse cascade invalidator failed",
			slog.Any("error", applyErr),
			slog.String("stage", stage),
			slog.String("subject_id", subjectID),
			slog.String("session_id", sessionID))
		return authRefreshRejected()
	}
	s.logger.Warn("session-refresh: reuse cascade applied",
		slog.String("stage", stage),
		slog.String("subject_id", subjectID),
		slog.String("session_id", sessionID))
	return authRefreshRejected()
}

// presentedSubjectID safely extracts SubjectID from a possibly-nil refresh.Token.
// refresh.Store implementations may return (nil, ErrReused) on malformed paths;
// downstream code must handle that case rather than panic.
func presentedSubjectID(t *refresh.Token) string {
	if t == nil {
		return ""
	}
	return t.SubjectID
}

// presentedSessionID is the SessionID counterpart of presentedSubjectID.
func presentedSessionID(t *refresh.Token) string {
	if t == nil {
		return ""
	}
	return t.SessionID
}

// refreshStoreError maps a refresh.Store error to the wire-layer error. Reuse
// detection (refresh.ErrReused) must NOT reach this helper — it is handled by
// handleReuseDetected so the cross-session cascade fires. A reuse error landing
// here would silently 401 without triggering the funnel; treat it as a
// programmer error and fall through to the unavailable branch with a loud log.
func (s *Service) refreshStoreError(logMessage string, err error) error {
	if errors.Is(err, refresh.ErrRejected) {
		return authRefreshRejected()
	}
	if errors.Is(err, refresh.ErrReused) {
		// Defensive: callers should have routed reuse to handleReuseDetected.
		// Log loudly so a regression is visible in production traces.
		s.logger.Error("session-refresh: ErrReused reached refreshStoreError — cascade NOT applied; check call site",
			slog.Any("error", err))
		return authRefreshRejected()
	}
	s.logger.Error(logMessage, slog.Any("error", err))
	return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
}

func authRefreshRejected() *errcode.Error {
	return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthRefreshFailed, errMsgInvalidRefreshToken)
}

// verifySession looks up the session row for a presented refresh token. It
// handles infra-error and not-found classes here (cascade-revoke on not-found);
// the session-revoked gate is enforced INLINE in refreshInTx (ADR §A11 重写
// 后, session-state 独立 funnel SESSION-REVOKED-FIELD-ACCESS-01). F4/F5
// extracted from Refresh to keep cognitive complexity within budget.
func (s *Service) verifySession(ctx context.Context, sessionID string) (*session.ValidateView, error) {
	sess, err := s.sessionStore.Get(ctx, sessionID)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-refresh: infra error on session lookup",
				slog.Any("error", err), slog.String("session_id", sessionID))
			return nil, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session lookup unavailable", err)
		}
		// F4: cascade-revoke on not-found; best-effort, fail-closed to 401.
		s.cascadeRevoke(ctx, sessionID, "session-not-found")
		return nil, authRefreshRejected()
	}
	return sess, nil
}

// cascadeRevoke routes security-response revokes (reuse attack,
// session-not-found, subject mismatch, user-not-active, stale-epoch) through
// RevokeSessionDetached. It is best-effort and fail-closed to 401: if
// RevokeSessionDetached fails the error is logged but NOT propagated — the
// rejection decision has already been made, and leaking the infra error as 503
// would (a) contradict the ADR §A13 single-envelope contract, and (b) signal
// cascade-state to the caller.
//
// Matches the fail-closed behavior already established by handleReuseDetected
// (which absorbs invalidator.Apply failures and returns authRefreshRejected()).
//
// reason is log-only and never exposed to callers.
//
// ref: golang/go context.WithoutCancel; hashicorp/vault token_store.go quitContext
// ref: ADR docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md
// ref: ADR §A13 single-envelope: 503 reserved for "cannot evaluate" (infra outage
//
//	before the decision); post-decision cascade failure must not promote to 503.
func (s *Service) cascadeRevoke(ctx context.Context, sessionID, reason string) {
	if err := s.refreshStore.RevokeSessionDetached(ctx, sessionID); err != nil {
		s.logger.Error("session-refresh: cascade revoke failed (fail-closed to 401)",
			slog.String("reason", reason),
			slog.Any("error", err),
			slog.String("session_id", sessionID))
		return
	}
	s.logger.Warn("session-refresh: cascade revoked refresh chain",
		slog.String("reason", reason),
		slog.String("session_id", sessionID))
}

// rejectIfRevokedSession runs the session-state inline check (RevokedAt)
// that ADR §A11 重写 moved out of the user-bound funnel. Extracted from
// refreshInTx to keep that function within the ≤15 cognitive-complexity
// budget after Wave 2 added the inline revoke + cascadeRevoke + log
// sequence.
//
// Owner package: this file is in the SESSION-REVOKED-FIELD-ACCESS-01
// allowlist (cells/accesscore/slices/sessionrefresh/), so the direct
// sess.RevokedAt read is legitimate. cascadeRevoke clears the refresh
// chain so subsequent rotation attempts immediately fail (post-§A11
// wire-uniformity 防枚举, ADR §A13).
//
// Returns nil when the session is live (refreshInTx continues to the
// subject-match / user-lookup / Assert / mint+rotate path). Returns a
// non-nil error when the session is revoked OR when cascadeRevoke
// surfaced an infra error — caller propagates as-is.
func (s *Service) rejectIfRevokedSession(ctx context.Context, sess *session.ValidateView, subjectID string) error {
	if sess.RevokedAt == nil {
		return nil
	}
	s.cascadeRevoke(ctx, sess.ID, "revoked-session")
	s.logger.Warn("session-refresh: revoked session rejected",
		slog.String("session_id", sess.ID),
		slog.String("subject_id", subjectID))
	return authRefreshRejected()
}

// rejectIfUserNotActive routes the baseline (user.CanAuthenticate via the
// funnel's implicit check) gate. On failure it cascade-revokes the refresh
// chain and returns uniform 401 ErrAuthRefreshFailed — aligned with ADR §A13
// single-envelope: user-not-active is indistinguishable to the wire caller
// from any other rejection reason (revoked/stale/reuse). OSS precedent:
// RFC 6749 §5.2 invalid_grant, ory-fosite ErrInvalidGrant, Keycloak
// invalid_grant — none differentiate account-status from token-invalidity.
// S4.0 fail-closed: a non-active user must not obtain a fresh access token;
// the cascade-revoke ensures subsequent rotation attempts immediately fail
// rather than keep returning new tokens.
//
// Ordering note (ADR §A11 重写): session-revoked is now checked INLINE in
// refreshInTx **before** the user is even loaded, so by the time this
// function runs the session is known not-revoked and the user is known
// fetched. The two-Assert-call pattern of the pre-§A11 form is collapsed
// to a single user-bound Assert; revoked sessions never reach here.
//
// Cascade scope is session-scoped only (cascadeRevoke against this
// sessionID); user-wide invalidation (epoch bump + RevokeForSubject) is
// not triggered here because "non-active user" is not a security event —
// account status changed via authzmutate which already ran the trifecta.
// Reuse-attack and stale-epoch paths route through invalidator.Apply for
// the user-wide cascade; this baseline gate is only refresh-chain cleanup.
func (s *Service) rejectIfUserNotActive(ctx context.Context, user *domain.User, sessionID string) error {
	if err := credentialauthority.Assert(user); err == nil {
		return nil
	}
	s.cascadeRevoke(ctx, sessionID, "user-not-active")
	// Emit the user dimension explicitly: cascadeRevoke logs only reason +
	// session_id, but operators auditing an account suspension need to trace
	// refresh-chain cleanup by user_id. Mirrors the subject_id/subject fields
	// that rejectIfRevokedSession / rejectIfStaleEpoch already log.
	s.logger.Warn("session-refresh: user-not-active rejected",
		slog.String("user_id", user.ID),
		slog.String("session_id", sessionID))
	return authRefreshRejected()
}

// rejectIfStaleEpoch detects a stale refresh grant: when
// presented.AuthzEpochAtIssue != user.AuthzEpoch(), the originating credential
// event (password change, account lock, role revoke) already ran the user-wide
// trifecta (epoch bump + RevokeForSubject all sessions + revoke all refresh
// chains). This refresh merely discovered the stale state after the fact.
//
// Stale epoch is NOT a reuse attack — routing it to handleReuseDetected would
// (a) emit CredentialEventRefreshReuse (a security ATTACK audit event) for
// benign post-credential-event churn, and (b) run a SECOND user-wide
// invalidation cascade (redundant; the credential event already did it).
// Instead, only defensively revoke THIS session's refresh chain and reject
// uniformly; no RefreshReuse audit event, no second epoch bump.
//
// Extracted from refreshInTx to keep that function within the cognitive-
// complexity budget (≤15) after S4d added the stale-epoch branch.
func (s *Service) rejectIfStaleEpoch(ctx context.Context, rowEpoch, userEpoch int64, sessionID, subjectID string) error {
	if rowEpoch == userEpoch {
		return nil
	}
	s.logger.Warn("session-refresh: stale authz epoch",
		slog.String("session_id", sessionID),
		slog.String("subject", subjectID),
		slog.Int64("row_epoch", rowEpoch),
		slog.Int64("user_epoch", userEpoch))
	s.cascadeRevoke(ctx, sessionID, "stale-epoch")
	return authRefreshRejected()
}

// fetchUserForRefresh reads the session's owning user so the caller can
// validate the per-refresh predicates (status='active', password-reset flag).
// Fail-closed: any error returns ErrAuthRefreshFailed so the caller aborts
// refresh rather than signing a token from stale or unknown user state.
func (s *Service) fetchUserForRefresh(ctx context.Context, sessionID, userID string) (*domain.User, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		s.logger.Error("session-refresh: failed to fetch user for refresh predicates (fail-closed)",
			slog.Any("error", err), slog.String("user_id", userID))
		if errcode.IsInfraError(err) {
			return nil, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session user unavailable", err)
		}
		s.cascadeRevoke(ctx, sessionID, "user-not-found")
		return nil, authRefreshRejected()
	}
	return user, nil
}
