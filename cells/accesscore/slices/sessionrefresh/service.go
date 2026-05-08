// Package sessionrefresh implements the session-refresh slice: validates an
// opaque refresh token via refresh.Store and issues a fresh access JWT.
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
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

// WithTxManager wires the cross-store TxRunner. The Refresh flow wraps the
// validate→update→rotate sequence in a single RunInTx so the session repo
// and refresh store updates share one commit boundary; nil tx is silently
// ignored to keep the option idempotent — final non-nil enforcement is in
// NewService.
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service implements token refresh logic.
type Service struct {
	sessionRepo  ports.SessionRepository
	userRepo     ports.UserRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	txRunner     persistence.TxRunner
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
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(sessionRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionrefresh.NewService: sessionRepo must not be nil")
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
		sessionRepo:  sessionRepo,
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
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s, err := NewService(sessionRepo, roleRepo, userRepo, refreshStore, issuer, logger, opts...)
	if err != nil {
		panic(errcode.Assertion("sessionrefresh: invariant violated: %v", err))
	}
	return s
}

// Refresh validates the presented opaque refresh token, checks the backing
// session and subject, mints a new access JWT, and only then commits refresh
// token rotation. Token rejection surfaces ErrAuthRefreshFailed; dependency
// failures surface ErrAuthRefreshUnavailable so clients do not confuse an
// outage with invalid credentials.
//
// Presenting an access JWT (or any string that does not parse as the opaque
// selector.verifier wire format) fails ParseOpaque inside refresh.Store and
// returns refresh.ErrRejected — the same fail-closed behavior the access-token
// confusion defense relies on.
//
// Cross-store ACID: the Peek → verifySession → fetchPasswordResetRequired →
// persistRefreshedSession → Rotate sequence runs inside a single
// txRunner.RunInTx, giving the rotate chain one commit boundary on PG-backed
// stores. The PG refresh store joins via savepoints and rolls back on outer
// abort. The session repo currently in production wiring is mem
// (cells/accesscore/internal/mem.SessionRepository), which does not honor TX
// rollback — its writes commit to the in-memory map immediately. Once B2
// lands postgres.PGSessionRepository, full cross-store ACID becomes effective
// without any change to this method. Cascade revokes go through
// refreshStore.RevokeSessionDetached, which intentionally bypasses the outer
// transaction (PR#395 detached-context invariant).
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

	s.logger.Info("token refreshed",
		slog.String("user_id", pair.UserID),
		slog.String("session_id", pair.SessionID))
	return pair, nil
}

// refreshInTx executes the validate→update→rotate sequence under the outer
// RunInTx boundary established by Refresh. With a real PG TxRunner
// (postgres.TxManager), store calls participate in the outer transaction via
// savepoint nesting and roll back together on abort; with a no-op TxRunner
// (cell.DemoTxRunner) the closure executes directly without TX semantics.
// Cascade-revoke calls intentionally bypass the outer TX through
// RevokeSessionDetached (PR#395 detached-context invariant).
func (s *Service) refreshInTx(ctx context.Context, refreshToken string) (dto.TokenPair, error) {
	presented, err := s.refreshStore.Peek(ctx, refreshToken)
	if err != nil {
		return dto.TokenPair{}, s.refreshStoreError("session-refresh: refresh store peek failed", err)
	}

	// Belt-and-braces: double-check the backing session has not been revoked
	// out-of-band (e.g. a logout that bypassed the refresh store).
	session, err := s.verifySession(ctx, presented.SessionID)
	if err != nil {
		return dto.TokenPair{}, err
	}
	if session.UserID != presented.SubjectID {
		if err := s.cascadeRevoke(ctx, presented.SessionID, "subject-mismatch"); err != nil {
			return dto.TokenPair{}, err
		}
		return dto.TokenPair{}, authRefreshRejected()
	}

	passwordResetRequired, err := s.fetchUserAndCheckActive(ctx, session.ID, session.UserID)
	if err != nil {
		return dto.TokenPair{}, err
	}

	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                session.UserID,
		SessionID:             session.ID,
		PasswordResetRequired: passwordResetRequired,
	})
	if err != nil {
		s.logger.Error("session-refresh: token issuance failed",
			slog.Any("error", err),
			slog.String("user_id", session.UserID),
			slog.String("session_id", session.ID))
		return dto.TokenPair{}, err
	}

	// Persist the session validation horizon before the final Rotate. With
	// the outer RunInTx in place, a Rotate failure rolls the session update
	// back as well — both stores share one commit boundary.
	if err := s.persistRefreshedSession(ctx, session, minted.AccessToken, minted.ExpiresAt); err != nil {
		return dto.TokenPair{}, err
	}

	newWire, rotated, err := s.refreshStore.Rotate(ctx, refreshToken)
	if err != nil {
		return dto.TokenPair{}, s.refreshStoreError("session-refresh: refresh store rotate failed", err)
	}
	if rotated.SessionID != session.ID || rotated.SubjectID != session.UserID {
		if err := s.cascadeRevoke(ctx, session.ID, "rotated-subject-mismatch"); err != nil {
			return dto.TokenPair{}, err
		}
		return dto.TokenPair{}, authRefreshRejected()
	}

	return dto.TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          newWire,
		ExpiresAt:             minted.ExpiresAt,
		SessionID:             session.ID,
		UserID:                session.UserID,
		PasswordResetRequired: passwordResetRequired,
	}, nil
}

func (s *Service) refreshStoreError(logMessage string, err error) error {
	if errors.Is(err, refresh.ErrRejected) {
		return authRefreshRejected()
	}
	s.logger.Error(logMessage, slog.Any("error", err))
	return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
}

func authRefreshRejected() *errcode.Error {
	return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthRefreshFailed, errMsgInvalidRefreshToken)
}

func (s *Service) persistRefreshedSession(ctx context.Context, session *domain.Session, accessToken string, expiresAt time.Time) error {
	refreshed := *session
	refreshed.AccessToken = accessToken
	refreshed.ExpiresAt = expiresAt
	if err := s.sessionRepo.Update(ctx, &refreshed); err != nil {
		if errcode.IsDomainNotFound(err, errcode.ErrSessionNotFound) {
			if revokeErr := s.cascadeRevoke(ctx, session.ID, "session-update-not-found"); revokeErr != nil {
				return revokeErr
			}
			return authRefreshRejected()
		}
		s.logger.Error("session-refresh: failed to persist refreshed session",
			slog.Any("error", err),
			slog.String("session_id", session.ID),
			slog.String("user_id", session.UserID))
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session update unavailable", err)
	}
	*session = refreshed
	return nil
}

// verifySession checks that the session backing a rotated token is live and
// cascade-revokes the refresh chain if it is not. Extracted from Refresh to
// stay within the cognitive-complexity budget (F4/F5).
func (s *Service) verifySession(ctx context.Context, sessionID string) (*domain.Session, error) {
	session, err := s.sessionRepo.GetByID(ctx, sessionID)
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
	if session.IsRevoked() {
		// F4: cascade-revoke on already-revoked session.
		if err := s.cascadeRevoke(ctx, sessionID, "revoked-session"); err != nil {
			return nil, err
		}
		return nil, authRefreshRejected()
	}
	return session, nil
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

// fetchUserAndCheckActive reads the user, fail-closes on any error, and
// rejects refresh + cascade-revokes the chain when the user is no longer
// active (locked or suspended). Refresh that proceeds for a locked user
// would silently mint a new access token despite the admin's lock decision
// — exactly the failure mode reviewer P2#7 flagged.
//
// Returns the PasswordResetRequired flag for the caller's claim emission.
func (s *Service) fetchUserAndCheckActive(ctx context.Context, sessionID, userID string) (bool, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		s.logger.Error("session-refresh: failed to fetch user (fail-closed)",
			slog.Any("error", err), slog.String("user_id", userID))
		if errcode.IsInfraError(err) {
			return false, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session user unavailable", err)
		}
		if err := s.cascadeRevoke(ctx, sessionID, "user-not-found"); err != nil {
			return false, err
		}
		return false, authRefreshRejected()
	}
	// P2#7: refuse refresh for locked/suspended users + cascade-revoke any
	// remaining session/refresh chain so a future refresh attempt with a
	// surviving wire token also fails. Unlike the other cascadeRevoke call
	// sites (where the session is already known to be missing/revoked), the
	// user-not-active path is reached with a still-active session row that
	// admin Lock may not have swept (manual status flip, mem-only Lock, etc.),
	// so we explicitly revoke the session here in addition to the refresh
	// chain. RevokeByIDAndOwner is idempotent on already-revoked rows.
	if user.Status != domain.StatusActive {
		s.logger.Warn("session-refresh: user not active; refusing refresh",
			slog.String("user_id", userID),
			slog.String("session_id", sessionID),
			slog.String("status", string(user.Status)),
		)
		if revokeErr := s.sessionRepo.RevokeByIDAndOwner(ctx, sessionID, userID); revokeErr != nil &&
			!errcode.IsDomainNotFound(revokeErr, errcode.ErrSessionNotFound) {
			s.logger.Error("session-refresh: failed to revoke session for inactive user",
				slog.Any("error", revokeErr),
				slog.String("session_id", sessionID))
			return false, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "session repo unavailable", revokeErr)
		}
		if err := s.cascadeRevoke(ctx, sessionID, "user-not-active"); err != nil {
			return false, err
		}
		return false, authRefreshRejected()
	}
	return user.PasswordResetRequired, nil
}
