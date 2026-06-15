package certsigning

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// Error codes minted by this package. The ERR_CERT_ namespace is registered in
// framework/pkg/errcode (gocellPlatformPrefixes) and locked by
// ERRCODE-PREFIX-OWNERSHIP-01; the golden prefix set is regenerated when the
// namespace is added. PR-5 emits only the construction-validation codes below —
// signing / revocation runtime codes (cross-scope denial, sign failure) land
// with their producing implementation (softca / PG, PR-6).
const (
	errCertScopeInvalidCode   = errcode.ErrCertScopeInvalid
	errCertRequestInvalidCode = errcode.ErrCertRequestInvalid
	errCertIssuedInvalidCode  = errcode.ErrCertIssuedInvalid
)

// Const-literal messages (MESSAGE-CONST-LITERAL-01: errcode messages must be
// const literals; runtime data flows through WithInternal / WithDetails). The
// short per-reason strings passed to the helpers below are themselves const
// literals at every call site.
const (
	msgScopeInvalid         = "certsigning: invalid certificate scope"
	msgRequestInvalid       = "certsigning: invalid certificate request"
	msgIssuedInvalid        = "certsigning: invalid issued certificate"
	msgScopeTenantInvalid   = "certsigning: certificate scope tenant invalid"
	msgSubjectTenantInvalid = "certsigning: certificate subject tenant invalid"
	msgCSRUnparseable       = "certsigning: csr is not a valid PKCS#10 request"
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
