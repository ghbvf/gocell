// Package sessionvalidate implements the session-validate slice: verifies
// access tokens and returns Claims. Implements runtime/auth.TokenVerifier.
package sessionvalidate

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// errMsgAuthFailed is the uniform error message for all session validation
// failures. Using a single message prevents session-state enumeration attacks.
const errMsgAuthFailed = "invalid or expired authentication token"

// Compile-time check: Service implements auth.TokenVerifier.
var _ auth.TokenVerifier = (*Service)(nil)

// Service validates JWT access tokens and checks session revocation status.
type Service struct {
	verifier    auth.IntentTokenVerifier
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewService creates a session-validate Service.
func NewService(verifier auth.IntentTokenVerifier, sessionRepo ports.SessionRepository, logger *slog.Logger) *Service {
	return &Service{verifier: verifier, sessionRepo: sessionRepo, logger: logger}
}

// Verify validates the token string and returns decoded Claims.
// It delegates JWT verification to the injected TokenVerifier (RS256) and
// additionally:
//   - requires the token to declare token_use=access (intent check); refresh
//     tokens replayed at business endpoints are rejected as invalid.
//   - checks session revocation status when the Extra["sid"] claim is present.
//
// All failure modes map to the uniform errMsgAuthFailed response to prevent
// token-type and session-state enumeration.
func (s *Service) Verify(ctx context.Context, tokenStr string) (auth.Claims, error) {
	claims, err := s.verifyJWTWithIntent(ctx, tokenStr)
	if err != nil {
		return auth.Claims{}, err
	}
	if s.sessionRepo == nil {
		return claims, nil
	}
	return s.enforceSessionState(ctx, claims)
}

// verifyJWTWithIntent runs the underlying verifier enforcing token_use=access
// at both the claim and JOSE header level, mapping all failures to the uniform
// ErrAuthInvalidToken response to prevent token-type enumeration.
func (s *Service) verifyJWTWithIntent(ctx context.Context, tokenStr string) (auth.Claims, error) {
	claims, err := s.verifier.VerifyIntent(ctx, tokenStr, auth.TokenIntentAccess)
	if err != nil {
		s.logger.Warn("session-validate: JWT verification failed",
			slog.Any("error", err))
		return auth.Claims{}, errcode.Wrap(errcode.ErrAuthInvalidToken, errMsgAuthFailed, err)
	}
	return claims, nil
}

// enforceSessionState performs the session-revocation / expiry checks that
// follow a successful JWT verification. Tokens missing the sid claim are
// rejected when sessionRepo is configured (fail-closed).
func (s *Service) enforceSessionState(ctx context.Context, claims auth.Claims) (auth.Claims, error) {
	sid, ok := claims.Extra["sid"].(string)
	if !ok || sid == "" {
		s.logger.Warn("session-validate: token missing sid",
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	session, err := s.sessionRepo.GetByID(ctx, sid)
	if err != nil {
		s.logSessionLookupError(sid, claims.Subject, err)
		return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	if session.IsRevoked() {
		s.logger.Warn("session-validate: revoked session used",
			slog.String("sid", sid),
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	if session.IsExpired() {
		s.logger.Warn("session-validate: expired session used",
			slog.String("sid", sid),
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	return claims, nil
}

// logSessionLookupError distinguishes "not found" (expected / logged at Warn)
// from infrastructure failures (Error) so dashboards can alert correctly.
func (s *Service) logSessionLookupError(sid, subject string, err error) {
	var ec *errcode.Error
	if errors.As(err, &ec) {
		s.logger.Warn("session-validate: session not found",
			slog.String("sid", sid),
			slog.String("subject", subject))
		return
	}
	s.logger.Error("session-validate: session repo unavailable",
		slog.String("sid", sid),
		slog.String("subject", subject),
		slog.Any("error", err))
}
