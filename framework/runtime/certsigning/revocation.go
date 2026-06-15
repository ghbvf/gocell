package certsigning

import (
	"context"
	"time"
)

// RevocationReason is the sealed RFC 5280 §5.3.1 CRL reason code. The value set
// is closed by the type system (single unexported field; the only constructors
// are the package-private singletons exposed via the accessor functions below),
// mirroring runtime/transport.TransportMode — an external package cannot mint an
// out-of-spec reason or pass a raw string where a RevocationReason is required.
// The zero value is invalid ([RevocationReason.IsZero]); it renders as the
// reasonUnknown sentinel rather than the empty string (fail-closed).
type RevocationReason struct{ v string }

// reasonUnknown is the fail-closed render of a zero-value RevocationReason. It is
// NOT a producible reason (absent from allRevocationReasons) — only the zero
// value renders as it, signaling a forged/uninitialized reason.
const reasonUnknown = "unknown"

// Package-private singletons — the sole RevocationReason values (RFC 5280
// §5.3.1). Exposed via accessor functions (not exported vars) so the registered
// values are immutable: reassigning a function is a compile error.
var (
	reasonUnspecified          = RevocationReason{v: "unspecified"}
	reasonKeyCompromise        = RevocationReason{v: "keyCompromise"}
	reasonCACompromise         = RevocationReason{v: "cACompromise"}
	reasonAffiliationChanged   = RevocationReason{v: "affiliationChanged"}
	reasonSuperseded           = RevocationReason{v: "superseded"}
	reasonCessationOfOperation = RevocationReason{v: "cessationOfOperation"}
	reasonCertificateHold      = RevocationReason{v: "certificateHold"}
	reasonRemoveFromCRL        = RevocationReason{v: "removeFromCRL"}
	reasonPrivilegeWithdrawn   = RevocationReason{v: "privilegeWithdrawn"}
	reasonAACompromise         = RevocationReason{v: "aACompromise"}
)

// ReasonUnspecified is RFC 5280 reason code 0.
func ReasonUnspecified() RevocationReason { return reasonUnspecified }

// ReasonKeyCompromise is RFC 5280 reason code 1. Use when the certificate's
// private key is known or suspected compromised.
func ReasonKeyCompromise() RevocationReason { return reasonKeyCompromise }

// ReasonCACompromise is RFC 5280 reason code 2 (the issuing CA key is compromised).
func ReasonCACompromise() RevocationReason { return reasonCACompromise }

// ReasonAffiliationChanged is RFC 5280 reason code 3.
func ReasonAffiliationChanged() RevocationReason { return reasonAffiliationChanged }

// ReasonSuperseded is RFC 5280 reason code 4 (a replacement certificate was issued).
func ReasonSuperseded() RevocationReason { return reasonSuperseded }

// ReasonCessationOfOperation is RFC 5280 reason code 5.
func ReasonCessationOfOperation() RevocationReason { return reasonCessationOfOperation }

// ReasonCertificateHold is RFC 5280 reason code 6 (temporary suspension; may be
// lifted via ReasonRemoveFromCRL).
func ReasonCertificateHold() RevocationReason { return reasonCertificateHold }

// ReasonRemoveFromCRL is RFC 5280 reason code 8 (remove a held entry — un-hold).
func ReasonRemoveFromCRL() RevocationReason { return reasonRemoveFromCRL }

// ReasonPrivilegeWithdrawn is RFC 5280 reason code 9. Use when a device's
// authorization to hold the certificate is withdrawn (MDM de-enrollment / ZT
// privilege revocation).
func ReasonPrivilegeWithdrawn() RevocationReason { return reasonPrivilegeWithdrawn }

// ReasonAACompromise is RFC 5280 reason code 10 (attribute-authority compromise).
func ReasonAACompromise() RevocationReason { return reasonAACompromise }

// String returns the reason mnemonic; a zero value renders as reasonUnknown
// (fail-closed), never the empty string.
func (r RevocationReason) String() string {
	if r.v == "" {
		return reasonUnknown
	}
	return r.v
}

// IsZero reports whether r is the invalid zero value.
func (r RevocationReason) IsZero() bool { return r.v == "" }

// allRevocationReasons is the closed registry of every producible reason. It
// backs the anti-vacuity freeze (a new reason added without registering here —
// or a renamed mnemonic — is caught by the archtest) and the [RevocationReasons]
// enumeration accessor.
var allRevocationReasons = []RevocationReason{
	reasonUnspecified,
	reasonKeyCompromise,
	reasonCACompromise,
	reasonAffiliationChanged,
	reasonSuperseded,
	reasonCessationOfOperation,
	reasonCertificateHold,
	reasonRemoveFromCRL,
	reasonPrivilegeWithdrawn,
	reasonAACompromise,
}

// RevocationReasons returns a defensive copy of the closed set of every RFC 5280
// §5.3.1 revocation reason this seam supports (for enumeration or adapter
// validation). Mutating the result does not affect the registry.
func RevocationReasons() []RevocationReason {
	return append([]RevocationReason(nil), allRevocationReasons...)
}

// RevokedCertificate is a read-only revocation-list entry: a revoked serial, its
// reason, and when it was revoked. It is an output DTO with exported fields (no
// sealing ceremony) — the security-bearing values it carries ([Serial],
// [RevocationReason]) are themselves sealed, so a forged RevokedCertificate
// still cannot name a forged serial or out-of-spec reason.
//
// RevokedAt MUST be UTC: implementations normalize to UTC before returning, so
// callers can compare it against the [RevocationStore.Tidy] before instant and
// CRL thisUpdate/nextUpdate without timezone ambiguity.
type RevokedCertificate struct {
	Serial    Serial
	Reason    RevocationReason
	RevokedAt time.Time
}

// RevocationStore is the certificate revocation + CRL seam. Every method takes a
// [CertScope] as a mandatory typed positional parameter (the tenant + issuer +
// device isolation domain), so revocation and CRL queries CANNOT be expressed by
// a bare serial — omitting the scope is a compile error and passing a raw string
// is a type error (CERT-REVOKE-SCOPED-01, Hard). Implementations (softca / PG,
// PR-6) MUST fail closed across isolation domains: a serial belonging to another
// tenant or issuer is invisible to Revoke / RevocationList — a caller never
// revokes or enumerates outside its scope (绝不凭裸 serial 跨隔离域).
type RevocationStore interface {
	// Revoke marks serial revoked within scope, with reason. Cross-scope serials
	// fail closed (the serial is treated as not found in scope).
	Revoke(ctx context.Context, scope CertScope, serial Serial, reason RevocationReason) error

	// RevocationList returns the revoked certificates within scope only.
	RevocationList(ctx context.Context, scope CertScope) ([]RevokedCertificate, error)

	// Tidy removes revocation records within scope whose certificates expired
	// before the given instant.
	Tidy(ctx context.Context, scope CertScope, before time.Time) error
}
