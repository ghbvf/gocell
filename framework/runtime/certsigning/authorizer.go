package certsigning

import (
	"context"
	"time"
)

// EnrollmentClaim is the sealed enrollment assertion presented to an
// [Authorizer]: which device (subject) under which isolation scope is asking for
// a certificate. It is a cross-boundary input decoded from an EST / SCEP
// front-end, so it is sealed (unexported fields + sole [NewEnrollmentClaim]
// minter) to prevent forging an elevated claim by composite literal.
type EnrollmentClaim struct {
	scope   CertScope
	subject DeviceSubject
}

// NewEnrollmentClaim returns a sealed EnrollmentClaim. The scope and subject
// must be non-zero; a claim cannot omit who is asking or under which isolation
// domain (fail-closed).
func NewEnrollmentClaim(scope CertScope, subject DeviceSubject) (EnrollmentClaim, error) {
	if scope.IsZero() {
		return EnrollmentClaim{}, errCertRequestInvalid("enrollment scope must not be empty")
	}
	if subject.IsZero() {
		return EnrollmentClaim{}, errCertRequestInvalid("enrollment subject must not be empty")
	}
	if err := requireSameOrigin(scope, subject); err != nil {
		return EnrollmentClaim{}, err
	}
	return EnrollmentClaim{scope: scope, subject: subject}, nil
}

// Scope returns the enrollment isolation scope.
func (c EnrollmentClaim) Scope() CertScope { return c.scope }

// Subject returns the enrolling device subject.
func (c EnrollmentClaim) Subject() DeviceSubject { return c.subject }

// SignConstraints is the sealed obligation an [Authorizer] returns on a granted
// enrollment and a [Signer] MUST enforce: the maximum certificate TTL and the
// allowed SANs. It is fail-closed by construction — the zero value is NOT
// granted ([SignConstraints.Granted] reports false), so an Authorizer that
// returns a zero SignConstraints (nil grant) denies signing rather than
// silently permitting an unconstrained certificate. Only [NewSignConstraints]
// mints a granted value.
type SignConstraints struct {
	granted     bool
	maxTTL      time.Duration
	allowedSANs SubjectAltNames
}

// NewSignConstraints returns a granted SignConstraints with a positive maximum
// TTL and the allowed SAN set. A non-positive maxTTL is rejected — a granted
// constraint with no positive lifetime ceiling is meaningless.
//
// Allowed-SAN semantics are deny-by-default: an empty allowedSANs
// ([SubjectAltNames.IsEmpty] true) means the Signer MUST NOT include any SAN —
// a request carrying SANs is rejected. To permit SANs the Authorizer must
// enumerate them explicitly. (An empty set is NOT "any SAN allowed".)
func NewSignConstraints(maxTTL time.Duration, allowedSANs SubjectAltNames) (SignConstraints, error) {
	if maxTTL <= 0 {
		return SignConstraints{}, errCertRequestInvalid("max ttl must be positive")
	}
	return SignConstraints{granted: true, maxTTL: maxTTL, allowedSANs: allowedSANs}, nil
}

// Granted reports whether these constraints authorize signing. The zero value
// returns false (fail-closed): a Signer must refuse unless Granted is true.
func (c SignConstraints) Granted() bool { return c.granted }

// MaxTTL returns the maximum certificate lifetime the Signer may grant.
func (c SignConstraints) MaxTTL() time.Duration { return c.maxTTL }

// AllowedSANs returns the SANs the Signer may include. Deny-by-default: an empty
// result means no SAN is permitted (see [NewSignConstraints]).
func (c SignConstraints) AllowedSANs() SubjectAltNames { return c.allowedSANs }

// Authorizer decides whether a device may obtain a certificate and, on a grant,
// returns the [SignConstraints] the [Signer] must enforce. It is INDEPENDENT of
// Signer (reusing the existing PDP): separating "may enroll" from "sign the
// CSR" keeps an authorization defect from widening into unauthorized signing.
//
// Authorization is fail-closed: an implementation denies by returning an error
// OR a zero (not-granted) SignConstraints; a Signer must verify Granted before
// minting.
type Authorizer interface {
	// AuthorizeEnroll evaluates claim and returns the signing constraints on a
	// grant. A nil-grant outcome is expressed as a non-granted SignConstraints
	// (or an error) — never an unconstrained permit.
	AuthorizeEnroll(ctx context.Context, claim EnrollmentClaim) (SignConstraints, error)
}

// AuthorizedCertRequest is a sealed signing request that has PASSED authorization:
// it can only be minted by [NewAuthorizedCertRequest] from a [CertRequest] plus a
// granted [SignConstraints], so a [Signer] can never be handed an un-authorized
// request. This carries the Authorizer's grant INTO the signing funnel as a
// type-level fact — the cert-manager/step-ca pattern (authorize produces
// constraints, sign enforces them), strengthened by Go's type system: there is
// no way to construct this value without satisfying the grant. The single field
// is unexported; the grant's obligations are enforced at construction, so an
// AuthorizedCertRequest is provably within its authorization.
type AuthorizedCertRequest struct {
	req CertRequest
}

// NewAuthorizedCertRequest combines a validated CertRequest with a granted
// SignConstraints, enforcing the authorization obligations fail-closed:
//   - the grant MUST be Granted (a zero / denied SignConstraints is rejected);
//   - the request TTL MUST NOT exceed the granted MaxTTL;
//   - every requested SAN MUST be within the granted AllowedSANs (deny-by-default:
//     an empty AllowedSANs permits no SAN).
//
// The result is the only value [Signer.Sign] accepts, so these checks cannot be
// skipped on the path to issuance (FR-005: SignConstraints enforced by the Signer
// boundary).
func NewAuthorizedCertRequest(req CertRequest, grant SignConstraints) (AuthorizedCertRequest, error) {
	if req.Scope().IsZero() {
		return AuthorizedCertRequest{}, errCertRequestInvalid("authorized request requires a valid request")
	}
	if !grant.Granted() {
		return AuthorizedCertRequest{}, errCertAuthorizeDenied("authorization not granted")
	}
	if req.TTL() > grant.MaxTTL() {
		return AuthorizedCertRequest{}, errCertConstraintViolation("requested ttl exceeds granted max ttl")
	}
	if !req.SubjectAltNames().subsetOf(grant.AllowedSANs()) {
		return AuthorizedCertRequest{}, errCertConstraintViolation("requested SAN outside granted allowance")
	}
	return AuthorizedCertRequest{req: req}, nil
}

// Request returns the authorized underlying request for the Signer to sign. Its
// TTL and SANs are provably within the grant that minted this value.
func (a AuthorizedCertRequest) Request() CertRequest { return a.req }
