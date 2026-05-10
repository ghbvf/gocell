// Package session declares the typed-Go-heavy Protocol primitive that bundles
// session-related protocol decisions for accesscore (and any future cell that
// owns server-side session state).
//
// This package is the protocol vocabulary plus a Store interface, an in-
// memory implementation (MemStore), and the Protocol-driven storetest
// conformance suite. The PG-backed Store implementation and the cell-side
// composition root wiring land in later phases of the same plan
// (docs/plans/202605082145-034-pg-corecell-b-route-plan.md, S3+S5 / S4).
//
// The protocol decisions encoded here are governed by:
//
//   - docs/architecture/202605101400-adr-credential-session-protocol.md
//     (D1 jti-only token model / D2 AuthzEpoch ordering /
//     D3 fail-closed credential events / D4 refresh-vs-session co-lifecycle /
//     D5 same-tx revocation / D6 sealed FingerprintMode)
//   - docs/architecture/202605101400-adr-admin-invariant.md (admin-related
//     domain rules; orthogonal to this package)
//   - docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md
//     (the typed-Go-heavy paradigm this package instantiates)
//
// Construction:
//
//	proto, err := session.NewProtocol(
//	    session.WithFingerprint(session.FingerprintJTIRef{}),
//	    session.WithOrdering(session.OrderingAuthzEpoch{}),
//	    session.WithRevokeOn(
//	        session.CredentialEventPasswordReset,
//	        session.CredentialEventLock,
//	        session.CredentialEventDelete,
//	        session.CredentialEventRoleRevoke,
//	    ),
//	)
//
// Composition root only (cmd/corebundle/access_module.go):
//
//	proto := session.MustNewProtocol(...)
//
// session.NewProtocol / MustNewProtocol must only be called from cmd/* (or
// from this package's own storetest sub-package, which constructs the
// canonical test Protocol) — cells must consume an injected *Protocol, never
// construct their own. This boundary is enforced by archtest
// SESSION-PROTOCOL-COMPOSITION-ROOT-01 (active; cell consumers begin
// arriving in S4 of the plan above).
package session
