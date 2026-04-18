// Package sessionrefresh implements the session-refresh slice: validates a
// refresh token and issues a new JWT token pair.
package sessionrefresh

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// WithUserRepository sets the UserRepository for sessionrefresh. Required when
// PasswordResetRequired propagation is needed (always in production).
func WithUserRepository(repo ports.UserRepository) Option {
	return func(s *Service) { s.userRepo = repo }
}

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	PasswordResetRequired bool
}

// Option configures a session-refresh Service.
type Option func(*Service)

// Service implements token refresh logic.
type Service struct {
	sessionRepo ports.SessionRepository
	userRepo    ports.UserRepository
	roleRepo    ports.RoleRepository
	issuer      *auth.JWTIssuer
	verifier    auth.IntentTokenVerifier
	logger      *slog.Logger
}

// NewService creates a session-refresh Service.
func NewService(
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	issuer *auth.JWTIssuer,
	verifier auth.IntentTokenVerifier,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		sessionRepo: sessionRepo,
		roleRepo:    roleRepo,
		issuer:      issuer,
		verifier:    verifier,
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Refresh validates the refresh token and issues a new token pair.
// Implements refresh token rotation: the old refresh token is invalidated
// after a successful refresh. If a previously rotated-out token is reused,
// the entire session is revoked (reuse detection).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, errcode.New(errcode.ErrAuthRefreshInvalidInput, "refresh token is required")
	}
	if err := s.verifyRefreshToken(ctx, refreshToken); err != nil {
		return nil, err
	}
	session, err := s.lookupSession(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if session.IsRevoked() {
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session has been revoked")
	}
	return s.rotateAndIssue(ctx, session)
}

// lookupSession retrieves the active session for the given refresh token.
// On session-not-found it checks for token reuse and revokes the session if detected.
func (s *Service) lookupSession(ctx context.Context, refreshToken string) (*domain.Session, error) {
	session, err := s.sessionRepo.GetByRefreshToken(ctx, refreshToken)
	if err == nil {
		return session, nil
	}
	// Check for refresh token reuse: if the token was previously valid
	// but has been rotated out, revoke the entire session to prevent
	// stolen token replay attacks.
	reuseSession, reuseErr := s.sessionRepo.GetByPreviousRefreshToken(ctx, refreshToken)
	if reuseErr != nil {
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session not found")
	}
	reuseSession.Revoke()
	if updateErr := s.sessionRepo.Update(ctx, reuseSession); updateErr != nil {
		s.logger.Error("session-refresh: failed to revoke session on token reuse",
			slog.Any("error", updateErr),
			slog.String("session_id", reuseSession.ID))
	}
	s.logger.Error("session-refresh: refresh token reuse detected, session revoked",
		slog.String("session_id", reuseSession.ID),
		slog.String("user_id", reuseSession.UserID))
	return nil, errcode.New(errcode.ErrAuthRefreshTokenReuse, "refresh token reuse detected, session revoked")
}

// rotateAndIssue mints new access + refresh tokens, persists the rotated
// session, and returns the resulting TokenPair.
func (s *Service) rotateAndIssue(ctx context.Context, session *domain.Session) (*TokenPair, error) {
	roleNames := s.fetchRoleNames(ctx, session.UserID)
	passwordResetRequired := s.fetchPasswordResetRequired(ctx, session.UserID)

	expiresAt := time.Now().Add(auth.DefaultAccessTokenTTL)

	accessToken, err := s.issueAccessToken(session.UserID, roleNames, session.ID, passwordResetRequired)
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue access token: %w", err)
	}

	newRefreshToken, err := s.issueRefreshToken(session.UserID, session.ID)
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue refresh token: %w", err)
	}

	session.PreviousRefreshToken = session.RefreshToken
	session.AccessToken = accessToken
	session.RefreshToken = newRefreshToken
	session.ExpiresAt = expiresAt

	if err := s.sessionRepo.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("session-refresh: persist: %w", err)
	}

	s.logger.Info("token refreshed", slog.String("user_id", session.UserID))

	return &TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          newRefreshToken,
		ExpiresAt:             expiresAt,
		PasswordResetRequired: passwordResetRequired,
	}, nil
}

// fetchRoleNames retrieves role names for the given user. Failures are logged
// at Warn level and an empty slice is returned so token issuance can proceed.
func (s *Service) fetchRoleNames(ctx context.Context, userID string) []string {
	roles, err := s.roleRepo.GetByUserID(ctx, userID)
	if err != nil {
		s.logger.Warn("session-refresh: failed to fetch roles",
			slog.Any("error", err), slog.String("user_id", userID))
		return nil
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names
}

// fetchPasswordResetRequired reads the current PasswordResetRequired flag from
// the user repo. Returns false on error or when no userRepo is wired.
// Failures are logged at Warn level.
func (s *Service) fetchPasswordResetRequired(ctx context.Context, userID string) bool {
	if s.userRepo == nil {
		return false
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		s.logger.Warn("session-refresh: failed to fetch user for reset flag",
			slog.Any("error", err), slog.String("user_id", userID))
		return false
	}
	return user.PasswordResetRequired
}

// verifyRefreshToken checks the JWT signature AND requires token_use=refresh.
//
// Enumeration defense: ErrAuthRefreshFailed is intentionally broader than
// ErrAuthInvalidTokenIntent and is used for ALL intent / signature / expiry
// failures to maintain enumeration defense parity between legitimate
// refresh-token-not-found and attacker-submitted access-token cases.
// The specific failure reason is recorded only in the structured log (Warn
// level) for ops visibility; the HTTP response always surfaces the generic
// ErrAuthRefreshFailed code so callers cannot distinguish token type from
// signature validity.
func (s *Service) verifyRefreshToken(ctx context.Context, refreshToken string) error {
	_, verifyErr := s.verifier.VerifyIntent(ctx, refreshToken, auth.TokenIntentRefresh)
	if verifyErr != nil {
		s.logger.Warn("session-refresh: refresh token verification failed",
			slog.Any("error", verifyErr))
		return errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
	}
	return nil
}

// issueAccessToken signs a short-lived JWT with intent=access carrying roles and
// the passwordResetRequired flag so middleware can enforce server-side reset.
func (s *Service) issueAccessToken(subject string, roles []string, sessionID string, passwordResetRequired bool) (string, error) {
	return s.issuer.Issue(auth.TokenIntentAccess, subject, auth.IssueOptions{
		Roles:                 roles,
		Audience:              []string{auth.DefaultJWTAudience},
		SessionID:             sessionID,
		PasswordResetRequired: passwordResetRequired,
	})
}

// issueRefreshToken signs a JWT with intent=refresh. Refresh tokens carry no
// roles: /auth/refresh refetches roles from the session's user on rotation.
func (s *Service) issueRefreshToken(subject, sessionID string) (string, error) {
	return s.issuer.Issue(auth.TokenIntentRefresh, subject, auth.IssueOptions{
		Audience:  []string{auth.DefaultJWTAudience},
		SessionID: sessionID,
	})
}
