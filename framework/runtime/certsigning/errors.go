package certsigning

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// Error codes minted by this package. The ERR_CERT_ namespace is registered in
// framework/pkg/errcode (gocellPlatformPrefixes) and locked by
// ERRCODE-PREFIX-OWNERSHIP-01; the golden prefix set is regenerated when the
// namespace is added. PR-5 emits the construction-validation codes (scope /
// request / issued invalid) plus the authorization-funnel codes (authorize
// denied / constraint violation, enforced by NewAuthorizedCertRequest). The
// signing-execution runtime codes (CA sign failure, cross-scope revocation
// denial) land with their producing implementation (softca / PG, PR-6).
const (
	errCertScopeInvalidCode        = errcode.ErrCertScopeInvalid
	errCertRequestInvalidCode      = errcode.ErrCertRequestInvalid
	errCertIssuedInvalidCode       = errcode.ErrCertIssuedInvalid
	errCertAuthorizeDeniedCode     = errcode.ErrCertAuthorizeDenied
	errCertConstraintViolationCode = errcode.ErrCertConstraintViolation
)

// Const-literal messages (MESSAGE-CONST-LITERAL-01: errcode messages must be
// const literals; runtime data flows through WithInternal / WithDetails). The
// short per-reason strings passed to the helpers below are themselves const
// literals at every call site.
const (
	msgScopeInvalid         = "certsigning: invalid certificate scope"
	msgRequestInvalid       = "certsigning: invalid certificate request"
	msgIssuedInvalid        = "certsigning: invalid issued certificate"
	msgAuthorizeDenied      = "certsigning: certificate enrollment not authorized"
	msgConstraintViolation  = "certsigning: request exceeds granted signing constraints"
	msgScopeTenantInvalid   = "certsigning: certificate scope tenant invalid"
	msgSubjectTenantInvalid = "certsigning: certificate subject tenant invalid"
	msgCSRUnparseable       = "certsigning: csr is not a valid PKCS#10 request"
	msgCSRSignatureInvalid  = "certsigning: csr signature is invalid (proof-of-possession failed)"
	msgCertUnparseable      = "certsigning: certificate is not valid DER"
)

// errCertScopeInvalid reports an invalid CertScope / IssuerID / DeviceID /
// Serial construction. The per-reason detail is server-only (WithInternal).
func errCertScopeInvalid(reason string) error {
	return errcode.New(errcode.KindInvalid, errCertScopeInvalidCode, msgScopeInvalid,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errCertRequestInvalid reports an invalid CertRequest / subject / SAN / usage.
func errCertRequestInvalid(reason string) error {
	return errcode.New(errcode.KindInvalid, errCertRequestInvalidCode, msgRequestInvalid,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errCertIssuedInvalid reports an invalid IssuedCert reconstruction (empty or
// unparseable certificate DER).
func errCertIssuedInvalid(reason string) error {
	return errcode.New(errcode.KindInvalid, errCertIssuedInvalidCode, msgIssuedInvalid,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errCertAuthorizeDenied reports that authorization was not granted for an
// AuthorizedCertRequest. KindPermissionDenied → HTTP 403.
func errCertAuthorizeDenied(reason string) error {
	return errcode.New(errcode.KindPermissionDenied, errCertAuthorizeDeniedCode, msgAuthorizeDenied,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errCertConstraintViolation reports that a request exceeds its granted signing
// constraints (TTL or SAN). KindPermissionDenied → HTTP 403.
func errCertConstraintViolation(reason string) error {
	return errcode.New(errcode.KindPermissionDenied, errCertConstraintViolationCode, msgConstraintViolation,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}
