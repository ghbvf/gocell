package auth

import (
	"context"
	"fmt"
	"slices"
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
	keys              VerificationKeyStore
	parserOpts        []jwt.ParserOption
	expectedAudiences []string
}

// JWTVerifierOption configures a JWTVerifier.
type JWTVerifierOption func(*JWTVerifier)

// WithExpectedAudiences configures VerifyIntent to enforce that the token's
// aud claim contains at least one of the given audience strings per RFC 8725
// §3.3 ("recipients MUST validate the aud claim"). This option is REQUIRED:
// NewJWTVerifier returns an error if no expected audiences are configured.
//
// The first argument is required (preventing zero-argument calls). Empty strings
// are silently filtered. Duplicate values across multiple calls are deduplicated.
//
// Verify() is never affected — audience enforcement is intentionally scoped to
// VerifyIntent only.
//
// ref: RFC 8725 §3.3, RFC 7519 §4.1.3 (aud may be string or array)
func WithExpectedAudiences(first string, rest ...string) JWTVerifierOption {
	return func(v *JWTVerifier) {
		for _, a := range append([]string{first}, rest...) {
			if a != "" && !slices.Contains(v.expectedAudiences, a) {
				v.expectedAudiences = append(v.expectedAudiences, a)
			}
		}
	}
}

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
	if len(v.expectedAudiences) == 0 {
		return nil, errcode.New(errcode.ErrAuthVerifierConfig,
			"JWT verifier requires at least one expected audience (WithExpectedAudiences); RFC 8725 §3.3")
	}
	return v, nil
}

// VerifyIntent validates the token and additionally requires the
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
	// Audience validation (RFC 8725 §3.3): when expectedAudiences is configured,
	// at least one must appear in the token's aud claim. The check is intentionally
	// placed after intent validation so intent-mismatch errors remain distinguishable
	// in structured logs (ops signal) even when audience would also fail.
	if len(v.expectedAudiences) > 0 && !audContainsAny(claims.Audience, v.expectedAudiences) {
		return Claims{}, errcode.Safe(errcode.ErrAuthInvalidTokenIntent,
			"token audience validation failed",
			fmt.Sprintf("aud %v does not satisfy any configured expected audience", claims.Audience))
	}
	return claims, nil
}

// audContainsAny reports whether any element of expected appears in aud.
// Per RFC 7519 §4.1.3 the aud claim may be a single string or an array;
// Claims.Audience always normalises it to []string (see parseAudience).
func audContainsAny(aud, expected []string) bool {
	for _, e := range expected {
		if slices.Contains(aud, e) {
			return true
		}
	}
	return false
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

// IssueOptions carries optional parameters for JWT issuance.
// Roles, Audience, and SessionID retain their original semantics.
// PasswordResetRequired is written as the "password_reset_required" claim
// only when true; when false the claim is omitted to keep the token compact.
//
// This struct replaces the previous 5-parameter Issue signature (backlog T2
// trigger: adding PasswordResetRequired would have been the 6th positional
// argument).
type IssueOptions struct {
	Roles                 []string
	Audience              []string
	SessionID             string
	PasswordResetRequired bool
}

// Issue creates a signed JWT token for the given subject and options.
//
// intent declares how the token is meant to be used (access vs. refresh).
// The resulting JWT carries both a JOSE "typ" header (at+jwt / refresh+jwt)
// and a "token_use" payload claim so verifiers can reject token-confusion
// attempts on two independent channels (RFC 9068 §2.1, RFC 8725 §3.11).
//
// The token header includes the kid of the active signing key. When
// opts.SessionID is non-empty, a "sid" claim binds the token to a specific
// session for revocation support.
//
// When opts.PasswordResetRequired is true, the claim "password_reset_required"
// is written into the token payload. When false (the zero value) the claim is
// omitted entirely for backward compatibility and to minimise token size.
func (i *JWTIssuer) Issue(intent TokenIntent, subject string, opts IssueOptions) (string, error) {
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
	if len(opts.Audience) > 0 {
		claims["aud"] = opts.Audience
	}
	if len(opts.Roles) > 0 {
		claims["roles"] = opts.Roles
	}
	if opts.SessionID != "" {
		claims["sid"] = opts.SessionID
	}
	if opts.PasswordResetRequired {
		claims["password_reset_required"] = true
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
	if sid, ok := mc["sid"].(string); ok {
		c.SessionID = sid
	}
	// password_reset_required is only written when true; absence means false
	// (backward compatible with tokens issued before Phase 3.5).
	if v, ok := mc["password_reset_required"].(bool); ok && v {
		c.PasswordResetRequired = true
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
	tokenUseClaim:             true,
	"sid":                     true,
	"password_reset_required": true,
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
