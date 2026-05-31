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
// ref: pkg/idutil.SafeID — sealed-newtype + UnmarshalJSON-fail-close范式.
type TenantID string

// String returns the underlying string.
func (t TenantID) String() string { return string(t) }

// Validate returns nil only for a non-empty, canonical-or-parseable UUID.
// The empty value is rejected (a tenant boundary cannot be absent at the
// point a TenantID is required); any non-UUID string is rejected.
func (t TenantID) Validate() error {
	return validateTenantIDString(string(t))
}

// UnmarshalJSON decodes data into t, failing closed if the JSON value is not a
// string OR if the decoded string is empty / not a valid UUID. On success the
// value is canonicalized to lowercase UUID form. Implements json.Unmarshaler.
func (t *TenantID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("tenant: TenantID: %w", err)
	}
	parsed, err := parseCanonical(raw)
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

// ParseTenantID validates s and returns a canonical (lowercase) TenantID.
// Empty input is an error (TenantID has no absent semantic). Non-UUID input
// is an error.
func ParseTenantID(s string) (TenantID, error) {
	return parseCanonical(s)
}

// parseCanonical is the single source of truth for TenantID's invariant:
// non-empty + valid UUID, normalized to canonical lowercase form.
func parseCanonical(s string) (TenantID, error) {
	if s == "" {
		return "", fmt.Errorf("tenant: TenantID required (empty)")
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return "", fmt.Errorf("tenant: TenantID must be a UUID: %w", err)
	}
	return TenantID(u.String()), nil
}

// validateTenantIDString validates without producing the canonical form, so
// Validate() can run on an already-constructed value (e.g. a struct field).
func validateTenantIDString(s string) error {
	_, err := parseCanonical(s)
	return err
}
