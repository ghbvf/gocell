// Package sessionvalidate implements the session-validate slice: verifies
// access tokens and returns Claims. Implements runtime/auth.TokenVerifier.
package sessionvalidate

import (
	"context"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	ErrValidateInvalidToken errcode.Code = "ERR_AUTH_INVALID_TOKEN"
)

// Compile-time check: Service implements auth.TokenVerifier.
var _ auth.TokenVerifier = (*Service)(nil)

// Service validates JWT access tokens and checks session revocation status.
type Service struct {
	signingKey  []byte
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewService creates a session-validate Service.
func NewService(signingKey []byte, sessionRepo ports.SessionRepository, logger *slog.Logger) *Service {
	return &Service{signingKey: signingKey, sessionRepo: sessionRepo, logger: logger}
}

// Verify validates the token string and returns decoded Claims.
func (s *Service) Verify(ctx context.Context, tokenStr string) (auth.Claims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errcode.New(ErrValidateInvalidToken, "unexpected signing method")
		}
		return s.signingKey, nil
	}, jwt.WithAudience("gocell"), jwt.WithIssuer("gocell-access-core"))
	if err != nil {
		return auth.Claims{}, errcode.New(ErrValidateInvalidToken, "invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return auth.Claims{}, errcode.New(ErrValidateInvalidToken, "invalid token claims")
	}

	claims := auth.Claims{
		Issuer: stringClaim(mapClaims, "iss"),
	}

	if sub, ok := mapClaims["sub"].(string); ok {
		claims.Subject = sub
	}

	if exp, err := mapClaims.GetExpirationTime(); err == nil && exp != nil {
		claims.ExpiresAt = exp.Time
	}

	if iat, err := mapClaims.GetIssuedAt(); err == nil && iat != nil {
		claims.IssuedAt = iat.Time
	}

	if aud, err := mapClaims.GetAudience(); err == nil {
		claims.Audience = aud
	}

	// Extract roles.
	if rolesRaw, ok := mapClaims["roles"].([]any); ok {
		for _, r := range rolesRaw {
			if rs, ok := r.(string); ok {
				claims.Roles = append(claims.Roles, rs)
			}
		}
	}

	// Check session revocation if sid claim is present.
	if sid, ok := mapClaims["sid"].(string); ok && sid != "" && s.sessionRepo != nil {
		session, err := s.sessionRepo.GetByID(ctx, sid)
		if err != nil {
			return auth.Claims{}, errcode.New(ErrValidateInvalidToken, "session not found")
		}
		if session.IsRevoked() {
			return auth.Claims{}, errcode.New(ErrValidateInvalidToken, "session has been revoked")
		}
	}

	return claims, nil
}

func stringClaim(m jwt.MapClaims, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// IssueTestToken creates a signed JWT for testing purposes.
// An optional sessionID can be provided to include the "sid" claim.
func IssueTestToken(signingKey []byte, subject string, roles []string, ttl time.Duration, sessionID ...string) (string, error) {
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
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(signingKey)
}
