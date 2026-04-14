package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/golang-jwt/jwt/v5"
)

// JWTVerifier verifies JWT tokens signed with RS256.
//
// ref: go-kratos/kratos middleware/auth/jwt/jwt.go -- JWT middleware pattern
// Adopted: KeyFunc-based verification, Claims extraction from context.
// Deviated: RS256 pinned (no configurable signing method), refuses HS256/none.
// Extended: kid-based key lookup from VerificationKeyStore (RFC 7638 thumbprint).
type JWTVerifier struct {
	keys VerificationKeyStore
}

// NewJWTVerifier creates a JWTVerifier that validates tokens by looking up the
// signing key from the VerificationKeyStore using the token's kid header.
func NewJWTVerifier(keys VerificationKeyStore) (*JWTVerifier, error) {
	if keys == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "verification key store must not be nil")
	}
	return &JWTVerifier{keys: keys}, nil
}

// Verify validates the token string and returns Claims on success.
// It rejects tokens that are not signed with RS256 or do not carry a valid kid.
func (v *JWTVerifier) Verify(_ context.Context, tokenStr string) (Claims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		// Inner errors use bare fmt.Errorf because jwt.Parse wraps them
		// and line 57 wraps the result with errcode. Using errcode here
		// would cause double-wrapping.

		// Pin to RS256 only -- reject HS256, none, and all other algorithms.
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
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
			return nil, fmt.Errorf("unknown kid: %s", kid)
		}
		return pub, nil
	})
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
}

// NewJWTIssuer creates a JWTIssuer using the active signing key from the provider.
func NewJWTIssuer(keys SigningKeyProvider, issuer string, ttl time.Duration) (*JWTIssuer, error) {
	if keys == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "signing key provider must not be nil")
	}
	return &JWTIssuer{
		keys:   keys,
		issuer: issuer,
		ttl:    ttl,
	}, nil
}

// Issue creates a signed JWT token for the given subject and roles.
// The token header includes the kid of the active signing key.
func (i *JWTIssuer) Issue(subject string, roles []string, audience []string) (string, error) {
	now := time.Now()
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
