// Package sessionrefresh implements the session-refresh slice: validates an
// opaque refresh token via refresh.Store and issues a fresh access JWT.
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	PasswordResetRequired bool
}

// Service implements token refresh logic.
type Service struct {
	sessionRepo  ports.SessionRepository
	userRepo     ports.UserRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	issuer       *auth.JWTIssuer
	logger       *slog.Logger
}

// NewService creates a session-refresh Service.
//
// userRepo is required (P1-3 fix): fetchPasswordResetRequired silently
// returns false when userRepo is nil, which bypasses the password-reset
// security gate.
//
// refreshStore owns both token-state validation and rotation — the slice
// no longer parses JWTs or performs application-layer reuse detection.
func NewService(
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
) *Service {
	return &Service{
		sessionRepo:  sessionRepo,
		roleRepo:     roleRepo,
		userRepo:     userRepo,
		refreshStore: refreshStore,
		issuer:       issuer,
		logger:       logger,
	}
}

// Refresh rotates the presented opaque refresh token and issues a new access
// JWT. Every non-happy path surfaces ErrAuthRefreshFailed; the specific reason
// is observable only through structured slog fields (enumeration defence).
//
// Presenting an access JWT (or any string that does not parse as the opaque
// selector.verifier wire format) fails ParseOpaque inside refresh.Store and
// returns refresh.ErrRejected — the same fail-closed behaviour as
// before (TestAuthIntent_AccessTokenBlockedAtRefreshPath).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if err := validation.RequireNotBlank(errcode.ErrAuthRefreshInvalidInput,
		validation.F("refreshToken", refreshToken),
	); err != nil {
		return nil, err
	}

	newWire, rotated, err := s.refreshStore.Rotate(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, refresh.ErrRejected) {
			return nil, errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
		}
		s.logger.Error("session-refresh: refresh store rotate failed",
			slog.Any("error", err))
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "refresh store unavailable")
	}

	// Belt-and-braces: double-check the backing session has not been revoked
	// out-of-band (e.g. a logout that bypassed the refresh store). If the
	// session is gone or revoked, cascade-revoke the refresh chain too so the
	// attacker cannot mint a second time.
	session, err := s.sessionRepo.GetByID(ctx, rotated.SessionID)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-refresh: infra error on session lookup",
				slog.Any("error", err), slog.String("session_id", rotated.SessionID))
			return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session lookup unavailable")
		}
		_ = s.refreshStore.RevokeSession(ctx, rotated.SessionID)
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session not found")
	}
	if session.IsRevoked() {
		_ = s.refreshStore.RevokeSession(ctx, rotated.SessionID)
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session has been revoked")
	}

	passwordResetRequired, err := s.fetchPasswordResetRequired(ctx, session.UserID)
	if err != nil {
		return nil, err
	}

	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
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
		return nil, err
	}

	s.logger.Info("token refreshed", slog.String("user_id", session.UserID))

	return &TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          newWire,
		ExpiresAt:             minted.ExpiresAt,
		PasswordResetRequired: passwordResetRequired,
	}, nil
}

// fetchPasswordResetRequired reads the current PasswordResetRequired flag
// from the user repo. Fail-closed: any error returns ErrAuthRefreshFailed so
// the caller aborts refresh rather than signing a token that omits the
// password_reset_required claim.
func (s *Service) fetchPasswordResetRequired(ctx context.Context, userID string) (bool, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		s.logger.Error("session-refresh: failed to fetch user for reset flag (fail-closed)",
			slog.Any("error", err), slog.String("user_id", userID))
		return false, errcode.New(errcode.ErrAuthRefreshFailed, "session user unavailable")
	}
	return user.PasswordResetRequired, nil
}
