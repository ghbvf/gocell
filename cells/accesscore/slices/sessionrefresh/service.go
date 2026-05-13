// Package sessionrefresh implements the session-refresh slice: validates an
// opaque refresh token via refresh.Store and issues a fresh access JWT.
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

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

// Service implements token refresh logic.
type Service struct {
	sessionStore session.Store
	userRepo     ports.UserRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	txRunner     persistence.CellTxManager
	issuer       *auth.JWTIssuer
	logger       *slog.Logger
	clock        clock.Clock
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

	var pair dto.TokenPair
	do := func(txCtx context.Context) error {
		var err error
		pair, err = s.refreshInTx(txCtx, refreshToken)
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
// session.Store is read-only on this path: refresh keeps session.ID stable
// across rotations (OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid
// stability). AuthzEpoch staleness is enforced by sessionvalidate (S4b).
func (s *Service) refreshInTx(ctx context.Context, refreshToken string) (dto.TokenPair, error) {
	presented, err := s.refreshStore.Peek(ctx, refreshToken)
	if err != nil {
		return dto.TokenPair{}, s.refreshStoreError("session-refresh: refresh store peek failed", err)
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
	if err := s.rejectIfUserNotActive(ctx, user, sess.ID); err != nil {
		return dto.TokenPair{}, err
	}
	passwordResetRequired := user.PasswordResetRequired

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
		return dto.TokenPair{}, s.refreshStoreError("session-refresh: refresh store rotate failed", err)
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

func (s *Service) refreshStoreError(logMessage string, err error) error {
	if errors.Is(err, refresh.ErrRejected) || errors.Is(err, refresh.ErrReused) {
		return authRefreshRejected()
	}
	s.logger.Error(logMessage, slog.Any("error", err))
	return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
}

func authRefreshRejected() *errcode.Error {
	return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthRefreshFailed, errMsgInvalidRefreshToken)
}

// verifySession checks that the session backing a rotated token is live and
// cascade-revokes the refresh chain if it is not. Extracted from Refresh to
// stay within the cognitive-complexity budget (F4/F5).
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
	if sess.RevokedAt != nil {
		// F4: cascade-revoke on already-revoked session.
		if err := s.cascadeRevoke(ctx, sessionID, "revoked-session"); err != nil {
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

// rejectIfUserNotActive cascade-revokes the refresh chain and returns
// ErrAuthUserNotActive (403) when the user is not in the 'active' state
// (suspended / locked). S4.0 fail-closed: a non-active user must not obtain
// a fresh access token; the cascade-revoke ensures subsequent rotation
// attempts immediately fail rather than keep returning new tokens.
// domain.User.CanAuthenticate() is the single source of truth shared with
// sessionlogin and sessionvalidate. Extracted to keep refreshInTx cognitive
// complexity ≤ 15 (.golangci.yml gocognit + sonar go:S3776).
func (s *Service) rejectIfUserNotActive(ctx context.Context, user *domain.User, sessionID string) error {
	if user.CanAuthenticate() {
		return nil
	}
	if err := s.cascadeRevoke(ctx, sessionID, "user-not-active"); err != nil {
		return err
	}
	return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
		"account is not active")
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
