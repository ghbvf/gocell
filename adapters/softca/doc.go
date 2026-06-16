// Package softca is the framework's built-in soft Certificate Authority (Epic
// #1895 US2 PR-6 #1902): a pure-standard-library implementation of the
// framework/runtime/certsigning seam (Signer + RevocationStore), so device
// certificates can be issued and revoked with NO external CA dependency. It is
// the "batteries-included" CA — the dev/test default before a real backend
// (step-ca / Vault PKI) is wired.
//
// # CA hierarchy
//
// softca runs a two-tier hierarchy (root CA → intermediate issuing CA), the
// standard PKI shape: the root is the long-lived offline-style trust anchor and
// the short-lived intermediate signs leaf device certificates and CRLs. This
// mirrors how a production CA isolates the trust anchor from the signing key.
//
// # Key custody — the core security invariant
//
// The CA signing private key (crypto.Signer) is held ONLY in this adapter and
// is structurally prevented from crossing the certsigning seam into
// kernel/runtime:
//
//   - The seam exposes Signer.Sign and Signer.TrustBundle — never a key getter
//     (upstream Hard, in framework/runtime/certsigning).
//
//   - This package exposes NO exported function or method that returns a
//     crypto.Signer or a raw private key. CA constructors take a key SOURCE
//     (generate-in-process, or PEM file paths), never a key value; the key
//     fields are unexported and consumed only package-internally by the signer
//     and CRL paths. A composition root wires softca by passing source config,
//     so it never holds the key (key custody stays here).
//
//   - KMS / HSM custody is intentionally ABSENT (not a silent no-op stub): real
//     hardware-backed key custody is a separate future adapter, designed when it
//     lands so the export surface for key injection is not prematurely widened.
//     A KMS-backed CA implements the same certsigning.Signer in its own
//     adapters/<kms> package — injecting the hardware-held crypto.Signer there —
//     rather than widening this package's API with a key setter.
//
//     ref: SPIRE pkg/server/ca (KeyManager — key never leaves the manager)
//     ref: step-ca authority/sign.go (Authority.Sign; private key adapter-held)
//     ref: cert-manager pkg/issuer (issuer holds signing material; API exposes Sign)
//
// # Pluggable issuance-record ledger
//
// The certsigning seam's Sign takes no epoch argument, yet a renewal must stamp
// a monotonic epoch (IssuedCert.Epoch), and RevocationStore.Tidy must drop
// revoked records whose certificate has expired — both need per-issuance
// metadata the seam does not carry. softca funnels that through ONE pluggable
// issuance-record store, [Ledger]: it records every issued certificate (deriving
// the per-scope renewal epoch) and the revocation state over those records.
// [Signer] (epoch) and [RevocationStore] (revoke / list / tidy) share a single
// Ledger instance — they are thin seam-facing implementations over this shared
// internal persistence, which additionally carries the epoch + notAfter the seam
// does not expose, so it is not a redundant re-layering of the seam. The default
// [MemLedger] is in-memory (a dev CA resets on restart); a PG-backed Ledger can
// be injected (as a required constructor argument) to persist epochs and
// revocations across restarts with no API change. A persistent Ledger is only
// meaningful alongside [NewFileCA] — pairing it with [NewDevCA] keeps epochs but
// rotates the CA key on restart, so the persisted records reference a vanished
// trust anchor.
//
// # Wiring
//
// Build the CA, then wire both seam halves over ONE shared Ledger via [NewSoftCA]
// (so revocation sees the certs the signer issued):
//
//	ca, err := softca.NewDevCA(clk)            // or NewFileCA(clk, dir)
//	signer, revStore, err := softca.NewSoftCA(clk, ca, softca.NewMemLedger())
//
// # Enforced invariants
//
// CERT-PRIVATE-KEY-CUSTODY-01 (upstream Hard / downstream Medium). Upstream
// Hard: the certsigning Signer interface has no key getter, so kernel/runtime
// cannot obtain the signing key THROUGH the seam (a compile-time fact), and
// softca declares no exported key getter. Downstream Medium: an archtest
// (tools/archtest/cert_invariants_test.go) scans every package importing
// certsigning for a private-key-typed struct field and allows only adapters/softca
// — defense in depth against a rogue key field appearing elsewhere in the cert
// subsystem. Blind spot: a private key smuggled as raw []byte (PEM) is not a
// typed-field match (the common ceiling of a field-type scan); and "only softca
// holds the key" is a caller/holder allowlist (Medium), not type-expressible,
// because any package can syntactically declare a crypto.Signer field.
//
// CERT-SIGN-FUNNEL-01 (downstream Medium). softca is the sanctioned minter:
// Signer.Sign returns certsigning.IssuedCert built via certsigning.NewIssuedCert,
// and the archtest caller-allowlist permits adapters/softca to call it. Business
// code obtains certificates only through Signer.Sign.
//
// Authoritative ratings, symbols, and blind spots are co-located in
// framework/runtime/certsigning/doc.go §Enforced invariants and
// tools/archtest/cert_invariants_test.go.
//
//	ref: docs/architecture/202606121500-1895-adr-device-cert-framework-pivot.md
package softca
