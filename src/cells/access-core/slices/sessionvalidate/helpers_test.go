package sessionvalidate

import (
	"crypto/rsa"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/runtime/auth"
)

// IssueTestToken creates a signed JWT for testing purposes.
// An optional sessionID can be provided to include the "sid" claim.
// signingKey must be *rsa.PrivateKey (RS256). The token includes a kid header
// derived from the public key via RFC 7638 thumbprint.
func IssueTestToken(signingKey *rsa.PrivateKey, subject string, roles []string, ttl time.Duration, sessionID ...string) (string, error) {
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

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = auth.Thumbprint(&signingKey.PublicKey)
	return token.SignedString(signingKey)
}
