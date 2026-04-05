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

const (
	ErrValidateInvalidToken errcode.Code = "ERR_AUTH_INVALID_TOKEN"
)

// Compile-time check: Service implements auth.TokenVerifier.
var _ auth.TokenVerifier = (*Service)(nil)

// Option configures a session-validate Service.
type Option func(*Service)

// WithSigningMethod overrides the default JWT verification method and key.
// For RS256, pass jwt.SigningMethodRS256 with an *rsa.PublicKey.
// For HS256 (default), pass jwt.SigningMethodHS256 with a []byte key.
func WithSigningMethod(method jwt.SigningMethod, key any) Option {
	return func(s *Service) {
		s.verifyMethod = method
		s.verifyKeyAny = key
	}
}

// Service validates JWT access tokens and checks session revocation status.
type Service struct {
	signingKey   []byte            // default HS256 key
	verifyMethod jwt.SigningMethod // overridden via WithSigningMethod
	verifyKeyAny any               // overridden via WithSigningMethod
	sessionRepo  ports.SessionRepository
	logger       *slog.Logger
}

// NewService creates a session-validate Service.
func NewService(signingKey []byte, sessionRepo ports.SessionRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{signingKey: signingKey, sessionRepo: sessionRepo, logger: logger}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Verify validates the token string and returns decoded Claims.
func (s *Service) Verify(ctx context.Context, tokenStr string) (auth.Claims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		// Accept configured signing method (e.g. RS256) when set.
		if s.verifyMethod != nil && s.verifyKeyAny != nil {
			if t.Method.Alg() != s.verifyMethod.Alg() {
				return nil, errcode.New(ErrValidateInvalidToken, "unexpected signing method")
			}
			return s.verifyKeyAny, nil
		}
		// Default: accept HMAC (HS256) only.
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
		return token.SignedString(k)
	case []byte:
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token.SignedString(k)
	default:
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token.SignedString(signingKey)
	}
}
