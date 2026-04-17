package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/golang-jwt/jwt/v5"
)

// DefaultAccessTokenTTL is the default time-to-live for access tokens issued
// by JWTIssuer. Shared across session-login, session-refresh, and bootstrap.
// Callers can override by passing a custom duration to NewJWTIssuer.
const DefaultAccessTokenTTL = 15 * time.Minute

// JWTVerifier verifies JWT tokens signed with RS256.
//
// ref: go-kratos/kratos middleware/auth/jwt/jwt.go -- JWT middleware pattern
// Adopted: KeyFunc-based verification, Claims extraction from context.
// Deviated: RS256 pinned (no configurable signing method), refuses HS256/none.
// Extended: kid-based key lookup from VerificationKeyStore (RFC 7638 thumbprint).
//
// ref: golang-jwt/jwt v5 parser_option.go -- WithTimeFunc for clock injection.
type JWTVerifier struct {
	keys       VerificationKeyStore
	parserOpts []jwt.ParserOption
}

// JWTVerifierOption configures a JWTVerifier.
type JWTVerifierOption func(*JWTVerifier)

// WithVerifierClock overrides the time source used for token expiry validation.
// Delegates to golang-jwt/jwt v5's WithTimeFunc ParserOption.
// A nil fn is ignored; the verifier uses time.Now by default.
func WithVerifierClock(fn func() time.Time) JWTVerifierOption {
	return func(v *JWTVerifier) {
		if fn != nil {
			v.parserOpts = append(v.parserOpts, jwt.WithTimeFunc(fn))
		}
	}
}

// NewJWTVerifier creates a JWTVerifier that validates tokens by looking up the
// signing key from the VerificationKeyStore using the token's kid header.
func NewJWTVerifier(keys VerificationKeyStore, opts ...JWTVerifierOption) (*JWTVerifier, error) {
	if keys == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "verification key store must not be nil")
	}
	v := &JWTVerifier{keys: keys}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

// Verify validates the token string and returns Claims on success.
// It rejects tokens that are not signed with RS256 or do not carry a valid kid.
func (v *JWTVerifier) Verify(_ context.Context, tokenStr string) (Claims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		// Inner errors use bare fmt.Errorf because jwt.Parse wraps them
		// and line 57 wraps the result with errcode. Using errcode here
		// would cause double-wrapping.

		// Pin to RS256 only -- reject HS256, RS384, RS512, none, and all others.
		// Type assertion (*jwt.SigningMethodRSA) would accept the entire RSA family;
		// we compare the concrete instance to reject RS384/RS512 explicitly.
		if token.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Extract kid from token header.
		kidRaw, ok := token.Header["kid"]
		if !ok {
			return nil, fmt.Errorf("missing kid header")
		}
		kid, ok := kidRaw.(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("invalid kid header")
		}

		pub, err := v.keys.PublicKeyByKID(kid)
		if err != nil {
			return nil, fmt.Errorf("key lookup failed for kid %s: %w", kid, err)
		}
		return pub, nil
	}, v.parserOpts...)
	if err != nil {
		return Claims{}, errcode.Wrap(errcode.ErrAuthUnauthorized, "token verification failed", err)
	}
	if !token.Valid {
		return Claims{}, errcode.New(errcode.ErrAuthUnauthorized, "invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, errcode.New(errcode.ErrAuthUnauthorized, "invalid token claims")
	}

	return mapClaimsToClaims(mapClaims), nil
}

// JWTIssuer signs JWT tokens with RS256 using the active key from a SigningKeyProvider.
// Each issued token carries a kid header derived from the signing key.
type JWTIssuer struct {
	keys   SigningKeyProvider
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

// JWTIssuerOption configures a JWTIssuer.
type JWTIssuerOption func(*JWTIssuer)

// WithIssuerClock overrides the time source used for iat/exp claim generation.
// A nil fn is ignored; the issuer uses time.Now by default.
func WithIssuerClock(fn func() time.Time) JWTIssuerOption {
	return func(i *JWTIssuer) {
		if fn != nil {
			i.now = fn
		}
	}
}

// NewJWTIssuer creates a JWTIssuer using the active signing key from the provider.
func NewJWTIssuer(keys SigningKeyProvider, issuer string, ttl time.Duration, opts ...JWTIssuerOption) (*JWTIssuer, error) {
	if keys == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "signing key provider must not be nil")
	}
	i := &JWTIssuer{
		keys:   keys,
		issuer: issuer,
		ttl:    ttl,
		now:    time.Now,
	}
	for _, o := range opts {
		o(i)
	}
	return i, nil
}

// Issue creates a signed JWT token for the given subject and roles.
// The token header includes the kid of the active signing key.
// When sessionID is non-empty, a "sid" claim is included to bind the token
// to a specific session for revocation support.
func (i *JWTIssuer) Issue(subject string, roles []string, audience []string, sessionID string) (string, error) {
	if i.keys.SigningKey() == nil {
		return "", errcode.New(errcode.ErrAuthKeyInvalid, "signing key is nil")
	}
	now := i.now()
	claims := jwt.MapClaims{
		"sub": subject,
		"iss": i.issuer,
		"iat": now.Unix(),
		"exp": now.Add(i.ttl).Unix(),
	}
	if len(audience) > 0 {
		claims["aud"] = audience
	}
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	if sessionID != "" {
		claims["sid"] = sessionID
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = i.keys.SigningKeyID()
	return token.SignedString(i.keys.SigningKey())
}

func mapClaimsToClaims(mc jwt.MapClaims) Claims {
	c := Claims{
		Extra: make(map[string]any),
	}

	if sub, ok := mc["sub"].(string); ok {
		c.Subject = sub
	}
	if iss, ok := mc["iss"].(string); ok {
		c.Issuer = iss
	}

	// Parse audience (can be string or []interface{}).
	switch aud := mc["aud"].(type) {
	case string:
		c.Audience = []string{aud}
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}

	// Parse roles ([]interface{}).
	if roles, ok := mc["roles"].([]any); ok {
		for _, r := range roles {
			if s, ok := r.(string); ok {
				c.Roles = append(c.Roles, s)
			}
		}
	}

	// Parse timestamps.
	if exp, ok := mc["exp"].(float64); ok {
		c.ExpiresAt = time.Unix(int64(exp), 0)
	}
	if iat, ok := mc["iat"].(float64); ok {
		c.IssuedAt = time.Unix(int64(iat), 0)
	}

	// Collect extra claims.
	standard := map[string]bool{"sub": true, "iss": true, "aud": true, "exp": true, "iat": true, "nbf": true, "roles": true}
	for k, v := range mc {
		if !standard[k] {
			c.Extra[k] = v
		}
	}

	return c
}
