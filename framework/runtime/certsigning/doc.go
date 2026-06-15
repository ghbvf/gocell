// Package certsigning is the framework's certificate-signing control-plane seam
// (Epic #1895 US2 PR-5 #1901): the CA signing interface, a protocol-agnostic
// request model, and the single mint funnel for device certificates. It defines
// the seam and its sealed value types ONLY — implementations land downstream
// (adapters/softca PR-6, runtime/certlifecycle PR-7, the EST front-end PR-8b,
// composition wiring PR-10).
//
// # Open-source alignment
//
// SPIRE ServerCA, cert-manager Sign, and step-ca Authority.Sign agree: the
// control plane (request model + signing seam) belongs to the framework; the
// signing private key (crypto.Signer) stays in an adapter and never crosses this
// boundary; and authorization ([Authorizer], "may this device enroll") is a
// SEPARATE interface from signing ([Signer], "sign this CSR"). This package
// encodes that split.
//
//	ref: SPIRE pkg/server/ca (ServerCA.Sign* — signing seam, key never exported)
//	ref: cert-manager pkg/controller/certificates (Authorize / Sign separation)
//	ref: step-ca authority/sign.go (SignConstraints / provisioner claims)
//
// # Core security invariants
//
//   - The signing private key never enters kernel/runtime. This seam exposes a
//     Sign operation and the trust bundle — never a key getter. (Key custody is
//     enforced adapter-side by CERT-PRIVATE-KEY-CUSTODY-01, PR-6.)
//   - [Signer.Sign] is the single sanctioned entry to mint a certificate, and
//     [NewIssuedCert] is the single minter of the sealed result (CERT-SIGN-FUNNEL-01).
//   - Signing and revocation share [CertScope] isolation: a bare serial never
//     crosses an isolation domain (RFC 5280 — issuer + serial is the unique
//     identity; serial alone is not). Revoke / RevocationList take CertScope as a
//     mandatory typed positional parameter (CERT-REVOKE-SCOPED-01).
//   - [Authorizer] and [Signer] are independent, so an authorization defect
//     cannot widen into unauthorized signing. [SignConstraints] is fail-closed:
//     its zero value is not granted.
//
// CertScope is also the isolation-invariant backstop for the #1899 status
// contract narrowing (http.deviceidentity.status.v1 is deviceId-only — a raw
// serial is never a query key); specific-certificate lookup downstream must
// carry a typed CertScope.
//
// # Enforced invariants
//
// CERT-VALUE-SEALED-CONSTRUCTION-01 (Hard). Every cross-trust-boundary input and
// minted-credential value type — [CertScope], [CertRequest], [IssuedCert],
// [DeviceSubject], [SubjectAltNames], [KeyUsages], [SignConstraints],
// [EnrollmentClaim], and the [IssuerID] / [DeviceID] / [Serial] newtypes — has
// only unexported fields, so a populated composite literal of any of them
// outside this package is a Go compile error. Forging certificate material or
// signing inputs is structurally unrepresentable; the sole minters are the
// New* constructors, which validate fail-closed. (Read-only OUTPUT types —
// [RevokedCertificate], the [Signer.TrustBundle] DER slice — are intentionally
// NOT sealed: they carry no forgeable security boundary.) The archtest is the
// reverse self-check that pins the seal against a regression that re-exports a
// field or adds a second minter surface.
//
// CERT-SIGN-FUNNEL-01 (funnel; upstream Hard, downstream Medium). Upstream Hard:
// [IssuedCert] / [CertRequest] field sealing makes forged signing material
// uncompilable. Downstream Medium: an archtest caller-allowlist restricts who
// may invoke [NewIssuedCert] (the mint funnel) to the sanctioned signer adapter
// (adapters/softca, PR-6; the allowlist is empty / forward in PR-5). Blind spot:
// [NewIssuedCert] MUST be exported because the signing adapter lives outside this
// package, so "only the sanctioned Signer mints certificates" is a Medium
// caller-allowlist, NOT Hard — there is no low-cost Hard path while the adapter
// is necessarily external. This is documented rather than packaged as a false
// Hard.
//
// CERT-REVOKE-SCOPED-01 (Hard). [RevocationStore].Revoke / RevocationList / Tidy
// take [CertScope] (and Revoke additionally [Serial]) as mandatory typed
// positional parameters: omitting the scope is a compile error (arity) and
// passing a raw string is a compile error (type). The compile-time type system
// is the guarantee; the archtest is the reverse self-check / anti-vacuity that
// fails if a refactor relaxes a signature back to a bare string or serial.
//
// Full invariant IDs, ratings, symbols, and blind spots are co-located in
// tools/archtest/cert_invariants_test.go.
//
// # Layering
//
// runtime/certsigning depends only on the standard library, crypto/x509, and
// framework/pkg (errcode, tenant). It defines interfaces an adapter implements;
// it imports no adapter, cell, or wiring package.
//
//	ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-004..007
//	ref: docs/architecture/202606130635-1939-adr-framework-owned-contract.md
package certsigning
