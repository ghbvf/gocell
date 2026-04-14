// Package sessionvalidate implements the session-validate slice: verifies
// access tokens and returns Claims. Implements runtime/auth.TokenVerifier.
package sessionvalidate

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)


// Compile-time check: Service implements auth.TokenVerifier.
var _ auth.TokenVerifier = (*Service)(nil)

// Service validates JWT access tokens and checks session revocation status.
type Service struct {
	verifier    auth.TokenVerifier
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewService creates a session-validate Service.
func NewService(verifier auth.TokenVerifier, sessionRepo ports.SessionRepository, logger *slog.Logger) *Service {
	return &Service{verifier: verifier, sessionRepo: sessionRepo, logger: logger}
}

// Verify validates the token string and returns decoded Claims.
// It delegates JWT verification to the injected TokenVerifier (RS256) and
// additionally checks session revocation status when the Extra["sid"] claim
// is present.
func (s *Service) Verify(ctx context.Context, tokenStr string) (auth.Claims, error) {
	claims, err := s.verifier.Verify(ctx, tokenStr)
	if err != nil {
		return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "invalid token")
	}

	// Fail-closed: when sessionRepo is configured, tokens MUST carry sid.
	if s.sessionRepo != nil {
		sid, ok := claims.Extra["sid"].(string)
		if !ok || sid == "" {
			s.logger.Warn("session-validate: token missing sid",
				slog.String("subject", claims.Subject))
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "invalid or expired authentication token")
		}
		session, err := s.sessionRepo.GetByID(ctx, sid)
		if err != nil {
			s.logger.Warn("session-validate: session not found",
				slog.String("sid", sid),
				slog.String("subject", claims.Subject))
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "invalid or expired authentication token")
		}
		if session.IsRevoked() {
			s.logger.Warn("session-validate: revoked session used",
				slog.String("sid", sid),
				slog.String("subject", claims.Subject))
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "invalid or expired authentication token")
		}
		if session.IsExpired() {
			s.logger.Warn("session-validate: expired session used",
				slog.String("sid", sid),
				slog.String("subject", claims.Subject))
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "invalid or expired authentication token")
		}
	}

	return claims, nil
}

