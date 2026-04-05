package auth

import (
	"context"
	"crypto/rsa"
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
type JWTVerifier struct {
	publicKey *rsa.PublicKey
}

// NewJWTVerifier creates a JWTVerifier that validates tokens using the given
// RSA public key with RS256 algorithm pinning.
func NewJWTVerifier(publicKey *rsa.PublicKey) *JWTVerifier {
	return &JWTVerifier{publicKey: publicKey}
}

// Verify validates the token string and returns Claims on success.
// It rejects tokens that are not signed with RS256.
func (v *JWTVerifier) Verify(_ context.Context, tokenStr string) (Claims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		// Pin to RS256 only -- reject HS256, none, and all other algorithms.
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return v.publicKey, nil
	})
	if err != nil {
		return Claims{}, errcode.Wrap(ErrAuthUnauthorized, "token verification failed", err)
	}
	if !token.Valid {
		return Claims{}, errcode.New(ErrAuthUnauthorized, "invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, errcode.New(ErrAuthUnauthorized, "invalid token claims")
	}

	return mapClaimsToClaims(mapClaims), nil
}

// ErrAuthUnauthorized is the error code for authentication failures.
var ErrAuthUnauthorized = errcode.Code("ERR_AUTH_UNAUTHORIZED")

// JWTIssuer signs JWT tokens with RS256 using an RSA private key.
type JWTIssuer struct {
	privateKey *rsa.PrivateKey
	issuer     string
	ttl        time.Duration
}

// NewJWTIssuer creates a JWTIssuer.
func NewJWTIssuer(privateKey *rsa.PrivateKey, issuer string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{
		privateKey: privateKey,
		issuer:     issuer,
		ttl:        ttl,
	}
}

// Issue creates a signed JWT token for the given subject and roles.
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
	return token.SignedString(i.privateKey)
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
