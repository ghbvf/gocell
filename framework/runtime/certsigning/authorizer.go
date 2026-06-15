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
