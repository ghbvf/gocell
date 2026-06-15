package certsigning

import "context"

// Signer is the CA signing seam (the cert-manager Sign / SPIRE ServerCA /
// step-ca Authority.Sign analog). It is the SINGLE entry point for minting a
// device certificate: a Signer takes a sealed [CertRequest] and returns a sealed
// [IssuedCert]. The implementation lives in an adapter (adapters/softca, PR-6),
// where the signing private key (crypto.Signer) is held and NEVER crosses this
// boundary — the interface exposes a Sign operation and the trust bundle, never
// a key getter.
//
// Sign is fail-closed: a Signer that cannot mint a certificate (CA unavailable,
// constraint violation) returns an error and never a partial / downgraded
// credential. Authorization (whether a device may obtain a certificate at all)
// is a SEPARATE concern handled by [Authorizer] — an authorization defect must
// not widen into unauthorized signing.
type Signer interface {
	// Sign mints a certificate for req and returns the sealed result. The
	// implementation reads the public key + proof-of-possession from the
	// request CSR but takes the subject / SANs / usages from the (constrained)
	// request itself, never blindly from the CSR.
	Sign(ctx context.Context, req CertRequest) (IssuedCert, error)

	// TrustBundle returns the CA trust anchors as DER-encoded certificates,
	// ordered leaf-issuer first to root last (the order an EST /cacerts response
	// is built from). It is a read-only output (no sealing ceremony) — the caller
	// verifies issued chains against it. Each element is a single certificate
	// DER; the EST front-end (PR-8b) is responsible for packaging them into the
	// RFC 7030 application/pkcs7-mime /cacerts response.
	TrustBundle(ctx context.Context) ([][]byte, error)
}
