package sessionvalidate

import (
	"crypto/rsa"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/runtime/auth"
)

// IssueTestToken creates a signed JWT for testing purposes with intent=access
// by default. Use IssueTestTokenWithIntent to build refresh or other intents.
//
// An optional sessionID can be provided to include the "sid" claim.
// signingKey must be *rsa.PrivateKey (RS256). The token includes a kid header
// derived from the public key via RFC 7638 thumbprint.
func IssueTestToken(signingKey *rsa.PrivateKey, subject string, roles []string, ttl time.Duration, sessionID ...string) (string, error) {
	return IssueTestTokenWithIntent(signingKey, auth.TokenIntentAccess, subject, roles, ttl, sessionID...)
}

// IssueTestTokenWithIntent signs a JWT with an explicit TokenIntent so tests
// can exercise intent-mismatch paths (e.g., refresh token at business endpoint).
// The resulting token carries both the token_use payload claim and the
// matching JOSE typ header expected by JWTVerifier.VerifyIntent.
func IssueTestTokenWithIntent(signingKey *rsa.PrivateKey, intent auth.TokenIntent, subject string, roles []string, ttl time.Duration, sessionID ...string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":       subject,
		"iat":       jwt.NewNumericDate(now),
		"exp":       jwt.NewNumericDate(now.Add(ttl)),
		"iss":       "gocell-access-core",
		"aud":       jwt.ClaimStrings{"gocell"},
		"token_use": string(intent),
	}
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	if len(sessionID) > 0 && sessionID[0] != "" {
		claims["sid"] = sessionID[0]
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = auth.Thumbprint(&signingKey.PublicKey)
	token.Header["typ"] = auth.TypHeaderForIntent(intent)
	return token.SignedString(signingKey)
}
