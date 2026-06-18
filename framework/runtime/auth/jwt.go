package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// DefaultAccessTokenTTL is the default time-to-live for access tokens issued
// by JWTIssuer.
const DefaultAccessTokenTTL = 15 * time.Minute

// msgTokenIntentFailed is the canonical client-visible message for token intent
// validation failures. All intent-check errcode sites must use this const so
// wire text stays identical (S1192).
const msgTokenIntentFailed = "token intent validation failed"

// JOSE typ header values written per TokenIntent. RFC 9068 §2.1 mandates
// "at+jwt" for access tokens.
const (
	jwtTypAccess = "at+jwt"
	// jwtTypEnroll is the JOSE typ header for a device first-enrollment credential
	// (TokenIntentEnrollment). A distinct typ keeps the typ↔token_use agreement
	// check (VerifyIntent) able to reject an enrollment credential replayed as an
	// access token on the JOSE-header channel as well as the claim channel.
	jwtTypEnroll    = "enroll+jwt"
	msgInvalidToken = "invalid token"
	// tokenUseClaim is the JWT payload key carrying the TokenIntent value.
	// Named after AWS Cognito's convention, which is the most widely-adopted
	// community precedent.
	tokenUseClaim = "token_use"
	// principalKindClaim is the JWT payload key carrying the PrincipalKindClaim
	// value (the signed device-vs-user marker; absent = user).
	principalKindClaim = "principal_kind"
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
	case TokenIntentEnrollment:
		return jwtTypEnroll
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
	case jwtTypEnroll:
		return TokenIntentEnrollment, true
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
// ref: coreos/go-oidc v3 oidc.go IDTokenVerifier -- issuer validation pattern.
type JWTVerifier struct {
	keys              VerificationKeyStore
	parserOpts        []jwt.ParserOption
	expectedAudiences []string
	expectedIssuer    string
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

// WithExpectedIssuer configures VerifyIntent to enforce that the token's iss
// claim exactly matches the given issuer string. The check is applied after
// audience validation so audience errors remain distinguishable in structured
// logs. An empty iss argument is silently ignored (no-op), preserving the
// previous behavior of accepting any issuer.
//
// ref: coreos/go-oidc v3 IDTokenVerifier — issuer validation with equality check
// ref: golang-jwt/jwt v5 WithIssuer ParserOption — functional option pattern
func WithExpectedIssuer(iss string) JWTVerifierOption {
	return func(v *JWTVerifier) { v.expectedIssuer = iss }
}

// NewJWTVerifier creates a JWTVerifier that validates tokens by looking up the
// signing key from the VerificationKeyStore using the token's kid header.
//
// clk is required; pass clock.Real() at the composition root or
// clockmock.New(...) in tests. Panics on nil or typed-nil clock.
//
// Rejects both plain-nil and typed-nil keys (e.g. var ks *KeySet = nil passed
// through an interface variable): a typed-nil would panic on the first method
// call downstream, so we fail fast at construction.
func NewJWTVerifier(keys VerificationKeyStore, clk clock.Clock, opts ...JWTVerifierOption) (*JWTVerifier, error) {
	clock.MustHaveClock(clk, "auth.NewJWTVerifier")
	if validation.IsNilInterface(keys) {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid, "verification key store must not be nil")
	}
	v := &JWTVerifier{keys: keys}
	v.parserOpts = append(v.parserOpts, jwt.WithTimeFunc(clk.Now))
	for _, o := range opts {
		o(v)
	}
	if len(v.expectedAudiences) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthVerifierConfig,
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
// HTTP middleware (expected=access). Refresh endpoints do not verify JWT
// refresh tokens; they pass opaque wire tokens to refresh.Store.
func (v *JWTVerifier) VerifyIntent(ctx context.Context, tokenStr string, expected TokenIntent) (Claims, error) {
	if !expected.IsValid() {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("unknown expected intent %q", string(expected)))),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	claims, header, err := v.parseAndVerify(ctx, tokenStr)
	if err != nil {
		return Claims{}, err
	}
	if !claims.TokenUse.IsValid() {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", "token_use claim missing or unknown")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	headerIntent, ok := intentForJWTTyp(stringFromHeader(header, "typ"))
	if !ok {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", "typ header missing or unknown")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	if headerIntent != claims.TokenUse {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", "typ header and token_use claim disagree")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	if claims.TokenUse != expected {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("token_use=%q does not match expected %q",
				string(claims.TokenUse), string(expected)))),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	// principal_kind is validated fail-closed in parseAndVerify
	// (validatePrincipalKind) on the raw claims map — absent / present-non-string
	// / present-unknown are all decided there, so no re-check is needed here.
	// Audience validation (RFC 8725 §3.3): when expectedAudiences is configured,
	// at least one must appear in the token's aud claim. The check is intentionally
	// placed after intent validation so intent-mismatch errors remain distinguishable
	// in structured logs (ops signal) even when audience would also fail.
	if len(v.expectedAudiences) > 0 && !audContainsAny(claims.Audience, v.expectedAudiences) {
		return Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			"token audience validation failed",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
				"aud %v does not satisfy any configured expected audience",
				claims.Audience))),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	// Issuer validation: when expectedIssuer is configured, the token's iss claim
	// must match exactly. Placed after audience check so each failure type remains
	// independently distinguishable in structured logs.
	if err := v.checkIssuer(claims); err != nil {
		return Claims{}, err
	}
	// Tenant claim is validated + canonicalized inside parseAndVerify (the decode
	// boundary where the raw claims map is available, so present-but-non-string
	// and present-but-empty are distinguishable from absent). By the time
	// VerifyIntent sees claims, claims.TenantID is either empty (single-tenant
	// path) or a canonical lowercase UUID — never an unvalidated string.
	return claims, nil
}

// checkIssuer validates that the token's iss claim matches the expected issuer.
// Returns ErrAuthInvalidTokenIntent with detail on mismatch.
//
// NOTE: The error detail distinguishes issuer mismatch from audience mismatch
// for ops-level logging and metrics, but both codes are collapsed to
// ERR_AUTH_UNAUTHORIZED at the HTTP response layer (see middleware.go).
// This is intentional enumeration defense — do NOT change the error code to
// leak more info to clients.
//
// ref: coreos/go-oidc v3 IDTokenVerifier.Verify — strict equality issuer check
func (v *JWTVerifier) checkIssuer(claims Claims) error {
	if v.expectedIssuer == "" {
		return nil
	}
	if claims.Issuer != v.expectedIssuer {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			"token issuer validation failed",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("iss %q does not match expected %q", claims.Issuer, v.expectedIssuer))),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	return nil
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
// the Claims and the raw JOSE header. It is the sole internal path of
// VerifyIntent (the only public verifier entry point), so the tenant-claim
// fail-closed gate it arms covers every JWT→Principal path.
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
		// Token-side errors (expired, unknown kid, wrong alg, malformed) are
		// always 401. Verifier-side infra errors (JWKS down, KMS unreachable,
		// signed-key cache miss with backing store failure) must surface as 503
		// so clients distinguish "wrong credentials" from "auth dependency
		// degraded" and operators alert correctly.
		//
		// IMPORTANT: do NOT use errcode.IsInfraError here. That predicate is
		// fail-closed — it treats every unclassified plain error (including
		// jwt.ErrTokenExpired, bare fmt.Errorf("invalid kid header"), and the
		// wrapped "unexpected signing method ...") as infra. JWT lib errors
		// arrive as plain errors by design (golang-jwt/jwt v5 exports them as
		// sentinel values), so a fail-closed check would mis-classify the
		// entire token-error surface as 503. Use an explicit Kind/Category
		// check that only fires when the underlying SigningKeyProvider
		// classified its own error as infra (Finding #1 PR #490 second review;
		// matches keycloak KeyManagementException, ory/fosite server_error
		// branch, zitadel caos_errs.IsInternal pattern).
		if hasExplicitInfraSignal(err) {
			return Claims{}, nil, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable,
				"authentication service unavailable", err,
				errcode.WithCategory(errcode.CategoryInfra))
		}
		return Claims{}, nil, errcode.Wrap(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "token verification failed", err)
	}
	if !token.Valid {
		return Claims{}, nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidToken)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid token claims")
	}

	claims, err := decodeAndValidateClaims(mapClaims)
	if err != nil {
		return Claims{}, nil, err
	}
	return claims, token.Header, nil
}

// decodeAndValidateClaims maps the raw claims to Claims and runs the fail-closed
// raw-map validators (tenant_id canonicalization, principal_kind closed-value
// set). Both validators read the RAW map to distinguish absent from
// present-but-malformed; collecting them here keeps parseAndVerify's cognitive
// complexity within the ≤15 ceiling.
func decodeAndValidateClaims(mc jwt.MapClaims) (Claims, error) {
	claims := mapClaimsToClaims(mc)
	if err := validateAndCanonicalizeTenant(mc, &claims); err != nil {
		return Claims{}, err
	}
	if err := validatePrincipalKind(mc, &claims); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

// validateAndCanonicalizeTenant fails closed on the tenant_id claim and, on
// success, rewrites claims.TenantID to its canonical lowercase UUID form. It
// reads the RAW claims map (not the lossy mapped string) so that the three
// cases are distinguishable:
//
//   - absent           → single-tenant path, claims.TenantID stays empty.
//   - present non-string → 401 (broken/forged or federated-IdP-misconfigured token).
//   - present string     → tenant.ParseTenantID rejects empty / non-canonical /
//     non-UUID values with 401; a valid value is canonicalized.
//
// All rejections use the generic unauthorized envelope (enumeration defense);
// the specific reason lives only in the server-side internal detail.
func validateAndCanonicalizeTenant(mc jwt.MapClaims, claims *Claims) error {
	raw, present := mc["tenant_id"]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			msgInvalidToken,
			errcode.WithInternal(errcode.InternalAttr("_", "tenant_id claim is not a string")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	tid, err := tenant.ParseTenantID(s)
	if err != nil {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			msgInvalidToken,
			errcode.WithInternal(errcode.InternalAttr("_", "tenant_id claim is not a valid UUID")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	claims.TenantID = tid.String()
	return nil
}

// validatePrincipalKind fails closed on the principal_kind claim. Like
// validateAndCanonicalizeTenant it reads the RAW claims map (not a lossy mapped
// value) so the three cases are distinguishable — the closed-value-set marker
// must be fully fail-closed (a malformed signed marker must NOT downgrade to the
// user default and bypass the device boundary):
//
//   - absent             → user default, claims.PrincipalKind stays empty.
//   - present non-string → 401 (broken/forged token).
//   - present string     → must be a known PrincipalKindClaim value; an unknown
//     value is 401 (so a typo can never silently fall through to a user mint).
//
// On success a known value is written to claims.PrincipalKind; the device issuer
// (mintDevicePrincipal) only acts on PrincipalKindClaimDevice. All rejections use
// the generic unauthorized envelope (enumeration defense); the specific reason
// lives only in the server-side internal detail.
func validatePrincipalKind(mc jwt.MapClaims, claims *Claims) error {
	raw, present := mc[principalKindClaim]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			msgInvalidToken,
			errcode.WithInternal(errcode.InternalAttr("_", "principal_kind claim is not a string")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	pk := PrincipalKindClaim(s)
	if !pk.IsValid() {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			msgInvalidToken,
			errcode.WithInternal(errcode.InternalAttr("_", "principal_kind claim is not a known value")),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	claims.PrincipalKind = pk
	return nil
}

// JWTIssuer signs JWT tokens with RS256 using the active key from a SigningKeyProvider.
// Each issued token carries a kid header derived from the signing key.
type JWTIssuer struct {
	keys            SigningKeyProvider
	issuer          string
	ttl             time.Duration
	clk             clock.Clock
	defaultAudience []string
}

// JWTIssuerOption configures a JWTIssuer.
type JWTIssuerOption func(*JWTIssuer)

// WithIssuerAudiencesFromSlice sets the audience written into tokens when
// IssueOptions.Audience is nil (fallback / default audience). The slice is
// copied defensively. Empty or nil slices are silently ignored.
//
// This is the canonical way to configure default audiences when constructing
// a JWTIssuer from a config.Registry. The config package uses this option
// internally; production code should prefer config.NewJWTIssuerFromRegistry
// instead of calling this directly.
func WithIssuerAudiencesFromSlice(auds []string) JWTIssuerOption {
	cp := make([]string, len(auds))
	copy(cp, auds)
	return func(i *JWTIssuer) {
		if len(cp) > 0 {
			i.defaultAudience = cp
		}
	}
}

// NewJWTIssuer creates a JWTIssuer using the active signing key from the provider.
//
// clk is required; pass clock.Real() at the composition root or
// clockmock.New(...) in tests. Panics on nil or typed-nil clock.
//
// Rejects both plain-nil and typed-nil keys (see NewJWTVerifier).
func NewJWTIssuer(keys SigningKeyProvider, issuer string, ttl time.Duration, clk clock.Clock, opts ...JWTIssuerOption) (*JWTIssuer, error) {
	clock.MustHaveClock(clk, "auth.NewJWTIssuer")
	if validation.IsNilInterface(keys) {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid, "signing key provider must not be nil")
	}
	i := &JWTIssuer{
		keys:   keys,
		issuer: issuer,
		ttl:    ttl,
		clk:    clk,
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
// This struct replaces the previous 5-parameter Issue signature (adding
// PasswordResetRequired would have been the 6th positional argument).
type IssueOptions struct {
	Roles                 []string
	Audience              []string
	SessionID             string
	PasswordResetRequired bool
	// JTI is the JWT ID ("jti" claim). When non-empty it is written into the
	// token payload. Empty string omits the claim.
	JTI string
	// TenantID is the tenant isolation boundary ("tenant_id" claim). When
	// non-empty it is written into the token payload so that the verifier can
	// extract it into ctxkeys.TenantID for every downstream service call.
	// Empty string omits the claim (single-tenant / pre-tenant paths).
	// The caller is responsible for passing a canonical UUID string; the issuer
	// trusts its caller and does not re-validate.
	TenantID string
	// PrincipalKind marks the token's principal kind ("principal_kind" claim).
	// The type is the wire enum PrincipalKindClaim (string), distinct from the
	// runtime PrincipalKind (int) enum — pass PrincipalKindClaimDevice, not
	// PrincipalDevice (which is the int constant for the runtime principal kind).
	// Empty (PrincipalKindClaimUser) omits the claim — an ordinary user token.
	// PrincipalKindClaimDevice mints a device bearer token; this REQUIRES a
	// non-empty TenantID in the same IssueOptions — a device principal is always
	// tenant-scoped, and the verifier rejects a device token without a tenant
	// claim fail-closed at runtime via mintDevicePrincipal. The issuer trusts its
	// caller (like TenantID) and does not re-validate; the verifier rejects an
	// unknown value fail-closed.
	PrincipalKind PrincipalKindClaim
}

// Issue creates a signed JWT token for the given subject and options.
//
// intent declares how the token is meant to be used. GoCell only issues access
// JWTs. The resulting JWT carries both a JOSE "typ" header (at+jwt)
// and a "token_use" payload claim so verifiers can reject token-confusion
// attempts on two independent channels (RFC 9068 §2.1, RFC 8725 §3.11).
//
// The token header includes the kid of the active signing key. When
// opts.SessionID is non-empty, a "sid" claim binds the token to a specific
// session for revocation support.
//
// When opts.PasswordResetRequired is true, the claim "password_reset_required"
// is written into the token payload. When false (the zero value) the claim is
// omitted entirely for backward compatibility and to minimize token size.
func (i *JWTIssuer) Issue(intent TokenIntent, subject string, opts IssueOptions) (string, error) {
	if !intent.IsValid() {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent,
			msgTokenIntentFailed,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("unknown token intent %q", string(intent)))),
			errcode.WithCategory(errcode.CategoryAuth))
	}
	if i.keys.SigningKey() == nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid, "signing key is nil")
	}
	now := i.clk.Now()
	expiry := i.ttl
	claims := jwt.MapClaims{
		"sub":         subject,
		"iss":         i.issuer,
		"iat":         now.Unix(),
		"exp":         now.Add(expiry).Unix(),
		tokenUseClaim: string(intent),
	}
	aud := opts.Audience
	if aud == nil {
		aud = i.defaultAudience // may be nil; upper layer decides whether to reject no-aud tokens
	}
	if len(aud) > 0 {
		claims["aud"] = aud
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
	if opts.JTI != "" {
		claims["jti"] = opts.JTI
	}
	if opts.TenantID != "" {
		claims["tenant_id"] = opts.TenantID
	}
	if opts.PrincipalKind != PrincipalKindClaimUser {
		claims[principalKindClaim] = string(opts.PrincipalKind)
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
	// principal_kind is NOT decoded here: a present-but-non-string claim must be
	// distinguishable from absent so it can fail closed (a malformed signed
	// marker must not silently downgrade to the user default). That requires the
	// RAW claims map, so validatePrincipalKind owns the full absent/malformed/
	// known-value decision (same boundary + reason as validateAndCanonicalizeTenant).
	if sid, ok := mc["sid"].(string); ok {
		c.SessionID = sid
	}
	// tenant_id is mapped verbatim here (pure decode, like sub/sid); the
	// authenticator validates/canonicalizes it via pkg/tenant.ParseTenantID and
	// fails closed on a malformed value. A non-string tenant_id leaves it empty.
	if tid, ok := mc["tenant_id"].(string); ok {
		c.TenantID = tid
	}
	// password_reset_required is only written when true; absence means false
	// (backward compatible with tokens issued before Phase 3.5).
	if v, ok := mc["password_reset_required"].(bool); ok && v {
		c.PasswordResetRequired = true
	}
	if jti, ok := mc["jti"].(string); ok {
		c.JTI = jti
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

var standardClaims = map[string]struct{}{
	"sub": {}, "iss": {}, "aud": {},
	"exp": {}, "iat": {}, "nbf": {}, "roles": {},
	tokenUseClaim:             {},
	principalKindClaim:        {},
	"sid":                     {},
	"tenant_id":               {},
	"password_reset_required": {},
	"jti":                     {},
	// S4d: authz_epoch removed from standardClaims so that any token still
	// carrying it (legacy / stray) surfaces in claims.Extra — making the
	// regression visible to TestRefresh_AccessJWT_NoAuthzEpochClaim. The mint
	// path no longer writes it; archtest JWT-CLAIMS-NO-AUTHZ-EPOCH-01
	// statically enforces the absence at the source.
}

func collectExtraClaims(mc jwt.MapClaims) map[string]any {
	extra := make(map[string]any)
	for k, v := range mc {
		if _, isStandard := standardClaims[k]; !isStandard {
			extra[k] = v
		}
	}
	return extra
}

// hasExplicitInfraSignal reports whether err carries an *errcode.Error in its
// wrapped chain that the originator EXPLICITLY classified as infrastructure —
// either by Kind = KindUnavailable or Category = CategoryInfra. This is the
// explicit-only counterpart to errcode.IsInfraError, which is fail-closed
// (any unclassified plain error → infra). The fail-closed semantics is wrong
// at the JWT verification boundary: jwt-library errors are plain errors by
// design (golang-jwt/jwt v5 returns sentinel jwt.ErrToken* and bare
// fmt.Errorf from keyfunc), so a fail-closed check would mis-tag every
// token-side validation error as 503.
//
// Scoped to package auth: kept local rather than promoted to pkg/errcode
// because the only valid use case is "JWT validation boundary" (matches
// keycloak's KeyManagementException isolation, ory/fosite Strategy interface
// split, zitadel caos_errs.IsInternal — see ADR note in jwt.go:280 block).
// Promoting it would create two near-identical predicates in pkg/errcode
// (IsInfraError vs HasInfraSignal) whose differences only ai-robust.md savvy
// AI co-authors would correctly pick — a Soft form to avoid per ai-robust.md.
func hasExplicitInfraSignal(err error) bool {
	if err == nil {
		return false
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Kind == errcode.KindUnavailable || ec.Category == errcode.CategoryInfra
}
