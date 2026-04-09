// Package sessionrefresh implements the session-refresh slice: validates a
// refresh token and issues a new JWT token pair.
package sessionrefresh

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	accessTokenTTL = 15 * time.Minute
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Option configures a session-refresh Service.
type Option func(*Service)

// Service implements token refresh logic.
type Service struct {
	sessionRepo ports.SessionRepository
	roleRepo    ports.RoleRepository
	issuer      *auth.JWTIssuer
	verifier    auth.TokenVerifier
	logger      *slog.Logger
}

// NewService creates a session-refresh Service.
func NewService(
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	issuer *auth.JWTIssuer,
	verifier auth.TokenVerifier,
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

	// Verify the refresh token JWT signature via RS256 verifier.
	_, err := s.verifier.Verify(ctx, refreshToken)
	if err != nil {
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
	}

	// Look up the session by current refresh token.
	session, err := s.sessionRepo.GetByRefreshToken(ctx, refreshToken)
	if err != nil {
		// Check for refresh token reuse: if the token was previously valid
		// but has been rotated out, revoke the entire session to prevent
		// stolen token replay attacks.
		if reuseSession, reuseErr := s.sessionRepo.GetByPreviousRefreshToken(ctx, refreshToken); reuseErr == nil {
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
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session not found")
	}

	if session.IsRevoked() {
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session has been revoked")
	}

	// Fetch roles for new access token.
	roles, _ := s.roleRepo.GetByUserID(ctx, session.UserID)
	roleNames := make([]string, 0, len(roles))
	for _, r := range roles {
		roleNames = append(roleNames, r.Name)
	}

	now := time.Now()
	expiresAt := now.Add(accessTokenTTL)

	accessToken, err := s.issueToken(session.UserID, roleNames)
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue access token: %w", err)
	}

	newRefreshToken, err := s.issueToken(session.UserID, nil)
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue refresh token: %w", err)
	}

	// Rotate refresh token: store old token for reuse detection.
	session.PreviousRefreshToken = session.RefreshToken
	session.AccessToken = accessToken
	session.RefreshToken = newRefreshToken
	session.ExpiresAt = expiresAt

	if err := s.sessionRepo.Update(ctx, session); err != nil {
		return nil, fmt.Errorf("session-refresh: persist: %w", err)
	}

	s.logger.Info("token refreshed", slog.String("user_id", session.UserID))

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *Service) issueToken(subject string, roles []string) (string, error) {
	return s.issuer.Issue(subject, roles, []string{"gocell"})
}
