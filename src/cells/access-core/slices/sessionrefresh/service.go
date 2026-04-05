// Package sessionrefresh implements the session-refresh slice: validates a
// refresh token and issues a new JWT token pair.
package sessionrefresh

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	ErrRefreshInvalidInput errcode.Code = "ERR_AUTH_REFRESH_INVALID_INPUT"
	ErrRefreshFailed       errcode.Code = "ERR_AUTH_REFRESH_FAILED"

	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Service implements token refresh logic.
type Service struct {
	sessionRepo ports.SessionRepository
	roleRepo    ports.RoleRepository
	signingKey  []byte
	logger      *slog.Logger
}

// NewService creates a session-refresh Service.
func NewService(
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	signingKey []byte,
	logger *slog.Logger,
) *Service {
	return &Service{
		sessionRepo: sessionRepo,
		roleRepo:    roleRepo,
		signingKey:  signingKey,
		logger:      logger,
	}
}

// Refresh validates the refresh token and issues a new token pair.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, errcode.New(ErrRefreshInvalidInput, "refresh token is required")
	}

	// Verify the refresh token JWT signature.
	_, err := jwt.Parse(refreshToken, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.signingKey, nil
	})
	if err != nil {
		return nil, errcode.New(ErrRefreshFailed, "invalid refresh token")
	}

	// Look up the session.
	session, err := s.sessionRepo.GetByRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, errcode.New(ErrRefreshFailed, "session not found")
	}

	if session.IsRevoked() {
		return nil, errcode.New(ErrRefreshFailed, "session has been revoked")
	}

	// Fetch roles for new access token.
	roles, _ := s.roleRepo.GetByUserID(ctx, session.UserID)
	roleNames := make([]string, 0, len(roles))
	for _, r := range roles {
		roleNames = append(roleNames, r.Name)
	}

	now := time.Now()
	expiresAt := now.Add(accessTokenTTL)

	accessToken, err := s.issueToken(session.UserID, roleNames, expiresAt, session.ID)
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue access token: %w", err)
	}

	newRefreshExpiry := now.Add(refreshTokenTTL)
	newRefreshToken, err := s.issueToken(session.UserID, nil, newRefreshExpiry, "")
	if err != nil {
		return nil, fmt.Errorf("session-refresh: issue refresh token: %w", err)
	}

	// Update session with new tokens.
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

func (s *Service) issueToken(subject string, roles []string, expiresAt time.Time, sid string) (string, error) {
	claims := jwt.MapClaims{
		"sub": subject,
		"iat": jwt.NewNumericDate(time.Now()),
		"exp": jwt.NewNumericDate(expiresAt),
		"iss": "gocell-access-core",
		"aud": jwt.ClaimStrings{"gocell"},
	}
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	if sid != "" {
		claims["sid"] = sid
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.signingKey)
}
