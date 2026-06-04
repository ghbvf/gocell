// Package tenant holds the layer-free tenancy value types shared across all
// GoCell layers: the TenantID sealed newtype (the multi-tenant isolation
// boundary identifier) and the RowScope authorization obligation enum.
//
// It lives under pkg/ (stdlib + google/uuid only) precisely because every
// layer — kernel/, runtime/, cells/, adapters/ — must be able to reference
// these types: TenantID becomes a mandatory typed positional parameter on
// tenant-scoped repo methods (PR-2; "漏传" = compile error), and RowScope
// becomes a typed obligation consumed by list/get repo methods (PR-4).
package tenant

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// TenantID identifies the tenant isolation boundary. Unlike idutil.SafeID —
// whose zero value is the legitimate "absent" representation — an empty
// TenantID is INVALID: a tenant-scoped query without a tenant cannot express
// a valid isolation predicate (Validate rejects it). The "no tenant"
// (single-tenant) case is represented at the ctx/wire layer by the absence of
// the key, never by an empty TenantID reaching a repo.
//
// A non-empty TenantID MUST be a canonical (lowercase) UUID. GoCell issues
// tenant identifiers itself and stores them in a PostgreSQL uuid column whose
// RLS predicate casts the app.tenant_id setting to uuid (PR-3), so the type
// boundary enforces the same UUID shape the database casts to.
//
// Empty/malformed rejection is RUNTIME fail-fast (Validate / ParseTenantID /
// UnmarshalJSON), not type-system Hard — an empty string is a legal Go
// expression. The type-system Hard guarantee ("漏传 tenant 编译失败") comes from
// repo methods taking TenantID as a mandatory positional parameter (PR-2).
//
// ref: pkg/idutil.SafeID — sealed-newtype + UnmarshalJSON fail-closed pattern.
type TenantID string

// canonicalUUIDLen is the length of a canonical dashed UUID
// (8-4-4-4-12 = 36 chars). The non-dashed/brace/urn variants that
// github.com/google/uuid.Parse also accepts are rejected — see parseCanonical.
const canonicalUUIDLen = 36

// String returns the underlying string.
func (t TenantID) String() string { return string(t) }

// Validate returns nil only for a non-empty TenantID that is ALREADY in
// canonical form: a 36-char dashed, lowercase UUID. The empty value is rejected
// (a tenant boundary cannot be absent at the point a TenantID is required), and
// any non-canonical form — including a valid-but-uppercase UUID — is rejected.
//
// Validate does NOT normalize the receiver: it asserts the value is canonical
// rather than coercing it. This makes Validate a true post-construction
// invariant check (e.g. on a TenantID repo parameter) — a value that passes is
// safe to use verbatim against the PostgreSQL uuid column / RLS predicate. To
// obtain the canonical lowercase form from a raw (possibly uppercase) string,
// use ParseTenantID, which normalizes.
func (t TenantID) Validate() error {
	return validateTenantIDString(string(t))
}

// UnmarshalJSON decodes data into t, failing closed if the JSON value is not a
// string OR if the decoded string is empty / not a valid UUID. On success the
// value is canonicalized to lowercase UUID form. Implements json.Unmarshaler.
//
// Routes through ParseTenantID (the untrusted-input constructor) so that the
// reserved nil-UUID sentinel is rejected at this boundary too — a JSON payload
// supplying "00000000-0000-0000-0000-000000000000" must not allow the caller to
// alias the system-tier config (F2 security fix).
func (t *TenantID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("tenant: TenantID: %w", err)
	}
	parsed, err := ParseTenantID(raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// MarshalJSON emits t as a JSON string. Implements json.Marshaler.
func (t TenantID) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(t))
}

// reservedNilUUID is the nil UUID string value reserved as SystemTenantID.
// It is named here — rather than referencing the constant from system_tenant.go —
// to keep pkg/tenant self-contained and to make the rejection guard a single
// string comparison with no circular-init risk.
const reservedNilUUID = "00000000-0000-0000-0000-000000000000"

// ParseTenantID validates s and returns a canonical (lowercase) TenantID.
// Empty input is an error (TenantID has no absent semantic). Non-UUID input
// is an error.
//
// SECURITY — untrusted-input boundary: ParseTenantID rejects the reserved
// nil-UUID sentinel ("00000000-0000-0000-0000-000000000000"). That sentinel is
// SystemTenantID, a tenant-isolation bypass token used exclusively by the
// internal control-plane read path. Allowing an external caller to supply it
// via a JWT claim or X-Tenant-ID header would let them alias the system-tier
// config, crossing the per-tenant boundary.
//
// Deliberate split from Validate:
//   - Validate() accepts SystemTenantID — repo param guards call Validate on
//     the typed param; the sentinel is a valid canonical TenantID value.
//   - ParseTenantID() rejects the nil-UUID — it is the parse-from-untrusted
//     constructor (JWT, header, UnmarshalJSON); the sentinel may never arrive
//     from external input.
//
// Internal code that needs to pass SystemTenantID to a repo must use the typed
// constant directly (tenant.SystemTenantID), never parse it from a string.
func ParseTenantID(s string) (TenantID, error) {
	if s == reservedNilUUID {
		return "", fmt.Errorf("tenant: TenantID %q is reserved for internal use and must not be submitted as external input", s)
	}
	return parseCanonical(s)
}

// parseCanonical is the single source of truth for TenantID's invariant:
// non-empty + canonical 36-char dashed UUID, normalized to lowercase.
//
// The explicit length guard is load-bearing: github.com/google/uuid.Parse is
// lenient — it also accepts 32-char compact ("3f25...3301"), brace-wrapped
// ("{...}"), and "urn:uuid:..." forms. A tenant boundary identifier must have a
// single canonical shape (the one the PostgreSQL uuid column / RLS cast round-
// trips), so anything that is not exactly 36 chars is rejected before Parse.
func parseCanonical(s string) (TenantID, error) {
	if s == "" {
		return "", fmt.Errorf("tenant: TenantID required (empty)")
	}
	if len(s) != canonicalUUIDLen {
		return "", fmt.Errorf("tenant: TenantID must be a canonical 36-char dashed UUID")
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return "", fmt.Errorf("tenant: TenantID must be a UUID: %w", err)
	}
	return TenantID(u.String()), nil
}

// validateTenantIDString asserts that s is ALREADY canonical: it must parse as
// a valid TenantID AND equal its own canonical form. parseCanonical normalizes
// (lowercases) on the way through, so comparing s to the canonical result is
// what rejects a valid-but-uppercase UUID — a check Validate needs but
// ParseTenantID / UnmarshalJSON (which deliberately normalize) must not apply.
func validateTenantIDString(s string) error {
	canonical, err := parseCanonical(s)
	if err != nil {
		return err
	}
	if string(canonical) != s {
		return fmt.Errorf("tenant: TenantID must be a canonical lowercase UUID; use ParseTenantID to normalize")
	}
	return nil
}
