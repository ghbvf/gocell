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

// WithClock sets the clock used for token expiry calculation.
// clk must not be nil; pass clock.Real() for production use.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		clock.MustHaveClock(clk, "sessionrefresh.WithClock")
		s.clock = clk
	}
}

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

// invalidatorApply is the minimal interface sessionrefresh needs from the
// credential-invalidation funnel. Using an interface (rather than a concrete
// *credentialinvalidate.Invalidator field) keeps the slice unit-testable with a
// spy and decouples it from the concrete invalidator package at the type level.
// Production code injects *credentialinvalidate.Invalidator which satisfies
// this interface by method set.
type invalidatorApply interface {
	Apply(ctx context.Context, subjectID string, event session.CredentialEvent) error
}

// WithInvalidator injects the credential-invalidation funnel used to
// cascade epoch bump + session revoke + refresh chain revoke on refresh-token
// reuse detection. Required — NewService fails fast when nil.
// Nil is silently ignored to keep the option idempotent; final nil
// enforcement is in NewService.
func WithInvalidator(inv *credentialinvalidate.Invalidator) Option {
	return func(s *Service) {
		if inv != nil {
			s.invalidator = inv
		}
	}
}

// Service implements token refresh logic.
type Service struct {
	sessionStore session.Store
	userRepo     ports.UserRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	txRunner     persistence.CellTxManager
	// invalidator is the credential-revocation funnel. Required — NewService
	// fails fast when nil. On refresh-token reuse detection, Apply is called
	// inside the outer transaction to atomically bump authz_epoch, revoke all
	// sessions, and revoke all refresh chains for the subject.
	invalidator invalidatorApply
	issuer      *auth.JWTIssuer
	logger      *slog.Logger
	clock       clock.Clock
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
	sessionStore session.Store,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(sessionStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: sessionStore must not be nil")
	}
	if validation.IsNilInterface(roleRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: roleRepo must not be nil")
	}
	if validation.IsNilInterface(userRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: userRepo must not be nil")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: refreshStore must not be nil")
	}
	if issuer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: issuer must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionStore: sessionStore,
		roleRepo:     roleRepo,
		userRepo:     userRepo,
		refreshStore: refreshStore,
		issuer:       issuer,
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sessionrefresh: TxRunner required; use WithTxManager")
	}
	if validation.IsNilInterface(s.invalidator) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sessionrefresh: Invalidator required; use WithInvalidator")
	}
	clock.MustHaveClock(s.clock, "sessionrefresh.NewService: clock required — use WithClock(c.clk)")
	return s, nil
}

// MustNewService is the static-wiring variant of NewService.
func MustNewService(
	sessionStore session.Store,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s, err := NewService(sessionStore, roleRepo, userRepo, refreshStore, issuer, logger, opts...)
	if err != nil {
		panic(panicregister.Approved("sessionrefresh-invariant", errcode.Assertion("sessionrefresh: invariant violated: %v", err)))
	}
	return s
}

// Refresh validates the presented opaque refresh token, checks the backing
// session and subject, mints a new access JWT, and rotates the refresh token.
// Token rejection surfaces ErrAuthRefreshFailed; dependency failures surface
// ErrAuthRefreshUnavailable so clients do not confuse an outage with invalid
// credentials.
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
	if err := validation.RequireNotEmpty(errcode.ErrAuthRefreshInvalidInput,
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
// a no-op TxRunner (cell.DemoTxRunner) the closure executes directly without
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
	if sess.SubjectID != presented.SubjectID {
		if err := s.cascadeRevoke(ctx, presented.SessionID, "subject-mismatch"); err != nil {
			return dto.TokenPair{}, err
		}
		return dto.TokenPair{}, authRefreshRejected()
	}

	user, err := s.fetchUserForRefresh(ctx, sess.ID, sess.SubjectID)
	if err != nil {
		return dto.TokenPair{}, err
	}
	// Two Assert call sites (read-side Hard funnel, ADR §A11):
	// 1. baseline (CanAuthenticate) first → dedicated 403 ErrAuthUserNotActive
	//    so an authenticated refresh caller learns "account no longer active"
	//    (semantically distinct from "invalid token").
	// 2. session-revoked second → uniform 401 ErrAuthRefreshFailed.
	//
	// Order matters: every Assert call runs the baseline as its first inline
	// check, so the session-gate Assert would also fail with baseline if a
	// suspended user reaches it. By running rejectIfUserNotActive first the
	// baseline failure maps to the dedicated 403 wire response; by the time
	// rejectIfSessionRevoked runs, baseline is known to pass.
	//
	// The funnel returns the same KindPermissionDenied/ErrAuthUserNotActive
	// envelope for both failures; the wire-level translation is decided BY
	// CALL SITE, not by inspecting err contents ("callers must not branch on
	// err" contract).
	if err := s.rejectIfUserNotActive(ctx, user, sess.ID); err != nil {
		return dto.TokenPair{}, err
	}
	if err := s.rejectIfSessionRevoked(ctx, user, sess); err != nil {
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
	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                sess.SubjectID,
		SessionID:             sess.ID,
		PasswordResetRequired: passwordResetRequired,
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
		if err := s.cascadeRevoke(ctx, sess.ID, "rotated-subject-mismatch"); err != nil {
			return dto.TokenPair{}, err
		}
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
	detachedCtx, cancel := ctxutil.WithDetachedTimeout(outerCtx, reuseCascadeTimeout)
	defer cancel()
	if applyErr := s.txRunner.RunInTx(detachedCtx, func(txCtx context.Context) error {
		return s.invalidator.Apply(txCtx, subjectID, session.CredentialEventRefreshReuse)
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
// the session-revoked gate is enforced downstream by credentialauthority.Assert
// alongside the user-status baseline so both checks live in one funnel. F4/F5
// extracted from Refresh to keep cognitive complexity within budget.
func (s *Service) verifySession(ctx context.Context, sessionID string) (*session.ValidateView, error) {
	sess, err := s.sessionStore.Get(ctx, sessionID)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-refresh: infra error on session lookup",
				slog.Any("error", err), slog.String("session_id", sessionID))
			return nil, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session lookup unavailable", err)
		}
		// F4: cascade-revoke on not-found; log the revoke error if it fails.
		if err := s.cascadeRevoke(ctx, sessionID, "session-not-found"); err != nil {
			return nil, err
		}
		return nil, authRefreshRejected()
	}
	return sess, nil
}

// cascadeRevoke routes security-response revokes (reuse attack,
// session-not-found, or subject mismatch) through RevokeSessionDetached. Once a
// cascade path is reached, the store owns the detached, 5-second bounded write
// policy that lets durable implementations persist the revoke outside the
// caller's cancellation and ambient transaction boundary.
//
// reason is log-only and never exposed to callers.
//
// ref: golang/go context.WithoutCancel; hashicorp/vault token_store.go quitContext
// ref: ADR docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md
func (s *Service) cascadeRevoke(ctx context.Context, sessionID, reason string) error {
	if err := s.refreshStore.RevokeSessionDetached(ctx, sessionID); err != nil {
		s.logger.Error("session-refresh: cascade revoke failed",
			slog.String("reason", reason),
			slog.Any("error", err),
			slog.String("session_id", sessionID))
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
	}
	s.logger.Warn("session-refresh: cascade revoked refresh chain",
		slog.String("reason", reason),
		slog.String("session_id", sessionID))
	return nil
}

// rejectIfSessionRevoked routes the session-revoked gate through
// credentialauthority.Assert (read-side Hard funnel, ADR §A11). On failure
// it cascade-revokes the refresh chain and returns the uniform 401
// (ErrAuthRefreshFailed) — matching the pre-migration behavior where a
// revoked session collapses into "invalid refresh token" without leaking
// the specific revocation reason.
//
// Note: this Assert also runs the baseline (CanAuthenticate) check, but a
// caller-not-active failure surfacing here is treated like
// session-revoked — uniform 401. The dedicated 403 baseline path lives in
// rejectIfUserNotActive below; ordering matters and is documented at the
// refreshInTx call site.
func (s *Service) rejectIfSessionRevoked(ctx context.Context, user *domain.User, sess *session.ValidateView) error {
	assertErr := credentialauthority.Assert(user,
		credentialauthority.SessionNotRevoked(sess))
	if assertErr == nil {
		return nil
	}
	if cascadeErr := s.cascadeRevoke(ctx, sess.ID, "revoked-session"); cascadeErr != nil {
		return cascadeErr
	}
	return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthRefreshFailed,
		errMsgInvalidRefreshToken,
		errcode.WithInternal("session-refresh: credentialauthority session gate failed: "+assertErr.Error()))
}

// rejectIfUserNotActive routes the baseline (user.CanAuthenticate via the
// funnel's implicit check) gate. On failure it cascade-revokes the refresh
// chain and returns the dedicated 403 (ErrAuthUserNotActive) — distinct
// from the uniform 401 used elsewhere because a refresh call is already
// authenticated (the caller proved holding a valid refresh token), so the
// signal "your account is no longer active" is semantically valuable to the
// admin/UI consumer of /refresh. S4.0 fail-closed: a non-active user must
// not obtain a fresh access token; the cascade-revoke ensures subsequent
// rotation attempts immediately fail rather than keep returning new tokens.
//
// The two-Assert-call pattern (baseline + session-revoked) is intentional:
// each Assert call site owns ONE wire-level translation (per the funnel's
// "callers MUST NOT branch on err" contract). The baseline gate runs FIRST
// (this function) so a still-live session with a now-suspended user
// surfaces the dedicated 403; the session-revoked gate runs second
// (rejectIfSessionRevoked) for any remaining revoked-row case → uniform
// 401. Ordering matters: every Assert runs baseline internally as the
// first inline check, so running session-revoked first would translate a
// suspended-user failure under the session-revoked envelope (401 instead
// of 403). The refreshInTx call site documents this constraint.
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
	if err := s.cascadeRevoke(ctx, sessionID, "user-not-active"); err != nil {
		return err
	}
	return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
		"account is not active")
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
	if err := s.cascadeRevoke(ctx, sessionID, "stale-epoch"); err != nil {
		return err
	}
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
		if err := s.cascadeRevoke(ctx, sessionID, "user-not-found"); err != nil {
			return nil, err
		}
		return nil, authRefreshRejected()
	}
	return user, nil
}
