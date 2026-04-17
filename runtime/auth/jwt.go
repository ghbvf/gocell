package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/golang-jwt/jwt/v5"
)

// DefaultAccessTokenTTL is the default time-to-live for access tokens issued
// by JWTIssuer. Can be overridden by cmd/* via NewJWTIssuer or WithRefreshTTL.
const DefaultAccessTokenTTL = 15 * time.Minute

// DefaultRefreshTokenTTL is the default time-to-live for refresh tokens issued
// by JWTIssuer. 7 days follows common OAuth 2.0 server defaults (e.g., Auth0,
// Okta, Keycloak). Can be overridden by cmd/* via WithRefreshTTL.
const DefaultRefreshTokenTTL = 7 * 24 * time.Hour

// JOSE typ header values written per TokenIntent. RFC 9068 §2.1 mandates
// "at+jwt" for access tokens; "refresh+jwt" is a GoCell convention (RFC 8725
// §3.11 permits issuer-chosen values as long as they are validated on read).
const (
	jwtTypAccess  = "at+jwt"
	jwtTypRefresh = "refresh+jwt"
	// tokenUseClaim is the JWT payload key carrying the TokenIntent value.
	// Named after AWS Cognito's convention, which is the most widely-adopted
	// community precedent.
	tokenUseClaim = "token_use"
)

// jwtTypForIntent returns the JOSE typ header value corresponding to intent.
// Returns empty string for unknown intents; callers must validate intent first.
func jwtTypForIntent(intent TokenIntent) string {
	return TypHeaderForIntent(intent)
}

// TypHeaderForIntent is the exported form of jwtTypForIntent, intended for
// test harnesses in sibling packages that need to build synthetic JWTs whose
// JOSE typ header matches what VerifyIntent expects. Production code should
// not call this — JWTIssuer.Issue writes the correct typ header automatically.
func TypHeaderForIntent(intent TokenIntent) string {
	switch intent {
	case TokenIntentAccess:
		return jwtTypAccess
	case TokenIntentRefresh:
		return jwtTypRefresh
	default:
		return ""
	}
}

// intentForJWTTyp is the reverse of jwtTypForIntent. Returns ("", false) for
// unrecognized typ values (which must be treated as fail-closed).
func intentForJWTTyp(typ string) (TokenIntent, bool) {
	switch typ {
	case jwtTypAccess:
		return TokenIntentAccess, true
	case jwtTypRefresh:
		return TokenIntentRefresh, true
	default:
		return "", false
	}
}

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
//
// Verify DOES NOT enforce token intent (access vs. refresh) — callers that
// need intent checks must use VerifyIntent instead.
func (v *JWTVerifier) Verify(ctx context.Context, tokenStr string) (Claims, error) {
	claims, _, err := v.parseAndVerify(ctx, tokenStr)
	return claims, err
}

// VerifyIntent validates the token like Verify, and additionally requires the
// declared intent (JWT token_use claim + JOSE typ header) to equal expected.
// Returns ErrAuthInvalidTokenIntent when:
//   - expected is not a valid TokenIntent
//   - the token lacks a token_use claim or typ header
//   - the token's claim/header disagree with each other
//   - the token's intent does not match expected
//
// This is the primary API for HTTP middleware (expected=access) and the
// /auth/refresh endpoint (expected=refresh).
func (v *JWTVerifier) VerifyIntent(ctx context.Context, tokenStr string, expected TokenIntent) (Claims, error) {
	if !expected.IsValid() {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			fmt.Sprintf("unknown expected intent %q", string(expected)))
	}
	claims, header, err := v.parseAndVerify(ctx, tokenStr)
	if err != nil {
		return Claims{}, err
	}
	if !claims.TokenUse.IsValid() {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			"token_use claim missing or unknown")
	}
	headerIntent, ok := intentForJWTTyp(stringFromHeader(header, "typ"))
	if !ok {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			"typ header missing or unknown")
	}
	if headerIntent != claims.TokenUse {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			"typ header and token_use claim disagree")
	}
	if claims.TokenUse != expected {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			fmt.Sprintf("token_use=%q does not match expected %q",
				string(claims.TokenUse), string(expected)))
	}
	return claims, nil
}

// stringFromHeader returns a string-typed JOSE header value or empty string.
func stringFromHeader(header map[string]any, key string) string {
	s, _ := header[key].(string)
	return s
}

// parseAndVerify decodes the token, validates its signature, and returns both
// the Claims and the raw JOSE header. It is the shared path of Verify and
// VerifyIntent.
func (v *JWTVerifier) parseAndVerify(_ context.Context, tokenStr string) (Claims, map[string]any, error) {
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
		return Claims{}, nil, errcode.Wrap(errcode.ErrAuthUnauthorized, "token verification failed", err)
	}
	if !token.Valid {
		return Claims{}, nil, errcode.New(errcode.ErrAuthUnauthorized, "invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, nil, errcode.New(errcode.ErrAuthUnauthorized, "invalid token claims")
	}

	return mapClaimsToClaims(mapClaims), token.Header, nil
}

// JWTIssuer signs JWT tokens with RS256 using the active key from a SigningKeyProvider.
// Each issued token carries a kid header derived from the signing key.
type JWTIssuer struct {
	keys       SigningKeyProvider
	issuer     string
	ttl        time.Duration
	refreshTTL time.Duration
	now        func() time.Time
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

// WithRefreshTTL overrides the default refresh token TTL (DefaultRefreshTokenTTL).
// A zero or negative duration is ignored.
func WithRefreshTTL(d time.Duration) JWTIssuerOption {
	return func(i *JWTIssuer) {
		if d > 0 {
			i.refreshTTL = d
		}
	}
}

// NewJWTIssuer creates a JWTIssuer using the active signing key from the provider.
func NewJWTIssuer(keys SigningKeyProvider, issuer string, ttl time.Duration, opts ...JWTIssuerOption) (*JWTIssuer, error) {
	if keys == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "signing key provider must not be nil")
	}
	i := &JWTIssuer{
		keys:       keys,
		issuer:     issuer,
		ttl:        ttl,
		refreshTTL: DefaultRefreshTokenTTL,
		now:        time.Now,
	}
	for _, o := range opts {
		o(i)
	}
	return i, nil
}

// Issue creates a signed JWT token for the given subject and roles.
//
// intent declares how the token is meant to be used (access vs. refresh).
// The resulting JWT carries both a JOSE "typ" header (at+jwt / refresh+jwt)
// and a "token_use" payload claim so verifiers can reject token-confusion
// attempts on two independent channels (RFC 9068 §2.1, RFC 8725 §3.11).
//
// The token header includes the kid of the active signing key. When
// sessionID is non-empty, a "sid" claim binds the token to a specific
// session for revocation support.
func (i *JWTIssuer) Issue(intent TokenIntent, subject string, roles []string, audience []string, sessionID string) (string, error) {
	if !intent.IsValid() {
		return "", errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token intent validation failed",
			fmt.Sprintf("unknown token intent %q", string(intent)))
	}
	if i.keys.SigningKey() == nil {
		return "", errcode.New(errcode.ErrAuthKeyInvalid, "signing key is nil")
	}
	now := i.now()
	expiry := i.ttl
	if intent == TokenIntentRefresh {
		expiry = i.refreshTTL
	}
	claims := jwt.MapClaims{
		"sub":         subject,
		"iss":         i.issuer,
		"iat":         now.Unix(),
		"exp":         now.Add(expiry).Unix(),
		tokenUseClaim: string(intent),
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
	token.Header["typ"] = jwtTypForIntent(intent)
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

	c.Audience = parseAudience(mc["aud"])
	c.Roles = parseStringSlice(mc["roles"])
	c.ExpiresAt = parseUnixTime(mc["exp"])
	c.IssuedAt = parseUnixTime(mc["iat"])
	if tu, ok := mc[tokenUseClaim].(string); ok {
		c.TokenUse = TokenIntent(tu)
	}
	c.Extra = collectExtraClaims(mc)

	return c
}

func parseAudience(v any) []string {
	switch aud := v.(type) {
	case string:
		return []string{aud}
	case []any:
		return filterStrings(aud)
	default:
		return nil
	}
}

func parseStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	return filterStrings(arr)
}

func filterStrings(arr []any) []string {
	var out []string
	for _, a := range arr {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func parseUnixTime(v any) time.Time {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(f), 0)
}

var standardClaims = map[string]bool{
	"sub": true, "iss": true, "aud": true,
	"exp": true, "iat": true, "nbf": true, "roles": true,
	tokenUseClaim: true,
}

func collectExtraClaims(mc jwt.MapClaims) map[string]any {
	extra := make(map[string]any)
	for k, v := range mc {
		if !standardClaims[k] {
			extra[k] = v
		}
	}
	return extra
}
