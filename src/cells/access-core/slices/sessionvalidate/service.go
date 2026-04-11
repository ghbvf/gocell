// Package sessionvalidate implements the session-validate slice: verifies
// access tokens and returns Claims. Implements runtime/auth.TokenVerifier.
package sessionvalidate

import (
	"context"
	"crypto/rsa"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

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

	// Check session revocation if sid claim is present in Extra.
	if sid, ok := claims.Extra["sid"].(string); ok && sid != "" && s.sessionRepo != nil {
		session, err := s.sessionRepo.GetByID(ctx, sid)
		if err != nil {
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "session not found")
		}
		if session.IsRevoked() {
			return auth.Claims{}, errcode.New(errcode.ErrAuthInvalidToken, "session has been revoked")
		}
	}

	return claims, nil
}

// IssueTestToken creates a signed JWT for testing purposes.
// An optional sessionID can be provided to include the "sid" claim.
// signingKey can be []byte (HS256) or *rsa.PrivateKey (RS256).
func IssueTestToken(signingKey any, subject string, roles []string, ttl time.Duration, sessionID ...string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": subject,
		"iat": jwt.NewNumericDate(now),
		"exp": jwt.NewNumericDate(now.Add(ttl)),
		"iss": "gocell-access-core",
		"aud": jwt.ClaimStrings{"gocell"},
	}
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	if len(sessionID) > 0 && sessionID[0] != "" {
		claims["sid"] = sessionID[0]
	}

	switch k := signingKey.(type) {
	case *rsa.PrivateKey:
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = auth.Thumbprint(&k.PublicKey)
		return token.SignedString(k)
	case []byte:
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token.SignedString(k)
	default:
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token.SignedString(signingKey)
	}
}
