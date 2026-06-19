package auth

// INVARIANT: ENROLLMENT-CREDENTIAL-MINT-CALLER-01
//
// # Enrollment-credential scheme (FR-012, epic #2299 G4, #2303)
//
// A device that has no certificate yet authenticates its FIRST EST enrollment
// (/simpleenroll) with a dedicated enrollment credential — NOT the setup
// bootstrap token (FMT-28 limits auth.bootstrap:true to /api/v{N}/*/setup/admin;
// EST is a different path, and reuse would be fail-closed). EnrollmentCredentialIssuer
// mints that credential; EnrollmentCredentialVerifier checks it and yields a
// sealed EnrollmentIdentity the EST front-end (G2/PR-8b) turns into a
// certsigning.EnrollmentClaim. Subsequent re-enroll uses the device's existing
// certificate (mTLS), not this credential.
//
// The credential is a short-lived, device-scoped, RS256-signed JWT carrying
// TokenIntentEnrollment. The two-channel token-confusion defense (JOSE typ
// header "enroll+jwt" + token_use="enrollment" claim, cross-checked in
// JWTVerifier.VerifyIntent) makes "an enrollment credential can never be used as
// a business access token, and vice-versa" a type/crypto fact — this IS FR-012's
// "dedicated scheme, not reused", with no runtime policy required.
//
// # Sealing / AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - EnrollmentIdentity has ONLY unexported fields and a single unexported
//     constructor (newEnrollmentIdentity, which fail-closes on empty tenant /
//     subject). An out-of-package composite literal can therefore only produce the
//     zero value (empty, accessors return ""), never a usable identity — so a
//     downstream that trusts an EnrollmentIdentity has proof it came through
//     verification. Out-of-package forgery of a usable identity is NOT expressible
//     (Hard). No separate seal sentinel is needed (unlike Principal.device, which
//     guards an EXPORTED Kind field).
//   - The enrollment-credential mint funnel — EnrollmentCredentialIssuer.Issue is
//     the sole sanctioned caller of JWTIssuer.Issue(TokenIntentEnrollment, ...) —
//     is Medium: Issue and TokenIntentEnrollment are necessarily exported (the
//     verifier needs the const), so Go visibility cannot express "only this
//     wrapper may pass the enrollment intent". The archtest backstop
//     ENROLLMENT-CREDENTIAL-MINT-CALLER-01 (tools/archtest) pins the call site and
//     the in-package EnrollmentIdentity construction to this file. Same documented
//     Go ceiling as COMMAND-ASYNC-EMIT-CALLER-01 / DEVICE-PRINCIPAL-MINT-CALLER-01.
//     The funnel's value is a single policy-enforcing mint path (short TTL + jti +
//     non-empty tenant/subject) that cannot drift.
//
// # One-time / replay (deferred to G2/PR-8b)
//
// Each credential carries a random jti (surfaced on EnrollmentIdentity.JTI) as a
// forward hook. Single-use / replay defense belongs at the EST /simpleenroll
// handler — where an enroll request (and thus a jti ledger / nonce) exists — and
// is intentionally NOT wired here (G4 has no request context to key it on).
//
// ref: smallstep/certificates — JWK provisioner token (short-lived, audience-bound
// JWT swapped once for a cert). ref: spiffe/spire — node join token (short-lived
// attestation credential). ref: RFC 7030 §3.2.3 (EST first-enroll bearer
// credential); RFC 8725 §3.11 (token-confusion isolation).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// EnrollmentCredentialTTL is the time-to-live of a device first-enrollment
// credential. Enrollment credentials are presented once, immediately, by a
// provisioning flow, so the window is deliberately short to bound the blast
// radius of a leaked credential (shorter than DefaultAccessTokenTTL).
const EnrollmentCredentialTTL = 5 * time.Minute

const (
	msgEnrollmentSubjectMissing = "enrollment credential subject missing"
	msgEnrollmentTenantInvalid  = "enrollment credential tenant must be a canonical tenant id"
	msgEnrollmentJTIMissing     = "enrollment credential jti missing"
	msgEnrollmentKindForbidden  = "enrollment credential is not device-scoped"
	msgEnrollmentVerifierNil    = "enrollment credential verifier requires a token verifier"
	msgEnrollmentJTIFailed      = "enrollment credential id generation failed"
)

// EnrollmentIdentity is the sealed result of verifying a device first-enrollment
// credential: the tenant and device subject the credential was minted for. All
// fields are unexported and the sole constructor is newEnrollmentIdentity, so an
// out-of-package literal can only be the (empty, inert) zero value — a usable
// identity proves it came through EnrollmentCredentialVerifier.Verify. The EST
// front-end (G2/PR-8b) maps it into a certsigning.EnrollmentClaim.
type EnrollmentIdentity struct {
	tenant  tenant.TenantID
	subject string
	jti     string
}

// Tenant returns the typed tenant isolation boundary the credential was minted
// for. It is a canonical tenant.TenantID (validated at construction), so the EST
// front-end (G2/PR-8b) can pass it straight into certsigning without re-parsing a
// naked string (tenancy.md: service APIs use typed tenant params, not raw string).
func (e EnrollmentIdentity) Tenant() tenant.TenantID { return e.tenant }

// Subject returns the device subject (device id) the credential was minted for.
func (e EnrollmentIdentity) Subject() string { return e.subject }

// JTI returns the credential's JWT ID — never empty for a verified identity (the
// constructor fails closed on a missing jti). It is the forward hook for the EST
// front-end's one-time / replay ledger: the consumer (G2/PR-8b) MUST consume this
// jti at /simpleenroll to reject replay within the TTL window; G4 only guarantees
// the jti is present, not consumed. Tracked: enrollment one-time/replay backlog
// (see ADR 202606121500-1895 Amendment 2026-06-18).
func (e EnrollmentIdentity) JTI() string { return e.jti }

// newEnrollmentIdentity is the SOLE constructor of a usable EnrollmentIdentity. It
// fails closed on a non-canonical/empty tenant (via tenant.ParseTenantID), an
// empty subject, or an empty jti, so a verified enrollment identity is always
// tenant-scoped (typed + canonical), device-identified, and replay-keyable
// (mirrors certsigning.NewEnrollmentClaim and mintDevicePrincipal). It exists only
// in this file; the archtest funnel ENROLLMENT-CREDENTIAL-MINT-CALLER-01 rejects
// any other in-package construction. rawTenant is the verified token's tenant
// claim (already canonical from VerifyIntent); it is re-parsed here as
// defense-in-depth and to produce the typed value.
func newEnrollmentIdentity(rawTenant, subject, jti string) (EnrollmentIdentity, error) {
	canonicalTenant, err := tenant.ParseTenantID(rawTenant)
	if err != nil {
		return EnrollmentIdentity{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentTenantInvalid,
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())))
	}
	if subject == "" {
		return EnrollmentIdentity{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentSubjectMissing)
	}
	if jti == "" {
		return EnrollmentIdentity{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentJTIMissing)
	}
	return EnrollmentIdentity{tenant: canonicalTenant, subject: subject, jti: jti}, nil
}

// EnrollmentCredentialIssuer mints device first-enrollment credentials. It is the
// sole sanctioned producer of TokenIntentEnrollment tokens
// (ENROLLMENT-CREDENTIAL-MINT-CALLER-01): it forces principal_kind=device, a
// short TTL, and a random jti, and fails closed on empty tenant/subject so a
// credential is always device-scoped and tenant-bound.
type EnrollmentCredentialIssuer struct {
	jwt *JWTIssuer
}

// NewEnrollmentCredentialIssuer builds an EnrollmentCredentialIssuer over an inner
// JWTIssuer constructed with EnrollmentCredentialTTL (NOT the 15-minute access
// issuer — JWTIssuer.Issue uses the construction-time TTL). It shares the signing
// keys / issuer string / clock with the rest of auth; the composition root
// (G2/PR-8b) decides the concrete wiring.
//
// opts are forwarded to the inner JWTIssuer and only influence audience/issuer
// declaration (e.g. WithIssuerAudiencesFromSlice); they do NOT override the TTL,
// which is fixed at EnrollmentCredentialTTL. Pass WithIssuerAudiencesFromSlice
// with the EST audience: a JWTVerifier requires an expected audience
// (NewJWTVerifier errors without one), so an issuer left without an audience mints
// credentials the verifier rejects on every call (a silent 401, hard to diagnose).
//
// clk is required; pass clock.Real() at the composition root or clockmock.New(...)
// in tests. Panics on nil or typed-nil clock.
func NewEnrollmentCredentialIssuer(
	keys SigningKeyProvider, issuer string, clk clock.Clock, opts ...JWTIssuerOption,
) (*EnrollmentCredentialIssuer, error) {
	clock.MustHaveClock(clk, "auth.NewEnrollmentCredentialIssuer")
	inner, err := NewJWTIssuer(keys, issuer, EnrollmentCredentialTTL, clk, opts...)
	if err != nil {
		return nil, err
	}
	return &EnrollmentCredentialIssuer{jwt: inner}, nil
}

// Issue mints a device first-enrollment credential for the given tenant and
// device subject. It fails closed on an empty subject or a non-canonical/empty
// tenant id (validated here at mint time so the issuer never produces a token the
// verifier is bound to reject). The returned token is a short-lived RS256 JWT with
// token_use=enrollment, principal_kind=device, and a random jti.
func (i *EnrollmentCredentialIssuer) Issue(tenantID, deviceSubject string) (string, error) {
	if deviceSubject == "" {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentSubjectMissing)
	}
	// Validate + canonicalize at mint time (defense-in-depth with VerifyIntent's
	// own tenant validation): a device credential is always tenant-scoped, and a
	// non-canonical tenant would only fail later at the verifier.
	canonicalTenant, err := tenant.ParseTenantID(tenantID)
	if err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentTenantInvalid,
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())))
	}
	jti, err := newEnrollmentJTI()
	if err != nil {
		return "", err
	}
	// Sole sanctioned JWTIssuer.Issue(TokenIntentEnrollment, ...) call site
	// (ENROLLMENT-CREDENTIAL-MINT-CALLER-01): device-scoped, short-TTL, jti-bound.
	return i.jwt.Issue(TokenIntentEnrollment, deviceSubject, IssueOptions{
		PrincipalKind: PrincipalKindClaimDevice,
		TenantID:      canonicalTenant.String(),
		JTI:           jti,
	})
}

// EnrollmentCredentialVerifier verifies a device first-enrollment credential and
// yields a sealed EnrollmentIdentity. It wraps any IntentTokenVerifier (the
// production verifier is *JWTVerifier) so the EST front-end (G2/PR-8b) shares the
// single canonical verification path.
type EnrollmentCredentialVerifier struct {
	verifier IntentTokenVerifier
}

// NewEnrollmentCredentialVerifier builds an EnrollmentCredentialVerifier over a
// token verifier. Fails closed (not a silent noop) on a nil/typed-nil verifier.
func NewEnrollmentCredentialVerifier(verifier IntentTokenVerifier) (*EnrollmentCredentialVerifier, error) {
	if validation.IsNilInterface(verifier) {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthVerifierConfig, msgEnrollmentVerifierNil)
	}
	return &EnrollmentCredentialVerifier{verifier: verifier}, nil
}

// Verify checks a device first-enrollment credential and returns the sealed
// EnrollmentIdentity it asserts. It fails closed when the credential is not an
// enrollment-intent token (TokenIntent isolation, via VerifyIntent), when it is
// not device-scoped (principal_kind != device), or when tenant/subject is empty
// (newEnrollmentIdentity). An access token presented here — or an enrollment
// credential presented at a business access endpoint — is rejected by the
// two-channel token-confusion defense in VerifyIntent.
func (v *EnrollmentCredentialVerifier) Verify(ctx context.Context, token string) (EnrollmentIdentity, error) {
	claims, err := v.verifier.VerifyIntent(ctx, token, TokenIntentEnrollment)
	if err != nil {
		return EnrollmentIdentity{}, err
	}
	// principal_kind absent/unknown is already fail-closed at decode
	// (validatePrincipalKind); here we only assert it IS device — an
	// enrollment credential must be device-scoped (not a user/service token).
	if claims.PrincipalKind != PrincipalKindClaimDevice {
		return EnrollmentIdentity{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgEnrollmentKindForbidden)
	}
	// claims.TenantID is canonical-UUID-or-empty (validated in VerifyIntent);
	// newEnrollmentIdentity fail-closes on empty tenant/subject.
	return newEnrollmentIdentity(claims.TenantID, claims.Subject, claims.JTI)
}

// newEnrollmentJTI returns a fresh random JWT ID for an enrollment credential.
func newEnrollmentJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errcode.New(errcode.KindInternal, errcode.ErrInternal, msgEnrollmentJTIFailed,
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())))
	}
	return hex.EncodeToString(b[:]), nil
}
