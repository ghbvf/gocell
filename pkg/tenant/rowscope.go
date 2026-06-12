package tenant

import "fmt"

// RowScope is the authorization obligation that decides the row-visibility
// predicate a tenant-scoped list/get repo method must apply. It is the GoCell
// typed projection of the XACML PDP→PEP obligation model (a bounded set of
// scopes, not an OPA-style partial-evaluation AST residue): the policy layer
// emits a RowScope, the repo layer (PEP) enforces it.
//
// RowScope lives in pkg/tenant only because it must be referenceable from
// every layer with no dependency cycle; it is NOT a tenancy primitive. Should
// the value set ever grow past these four, the migration target is pkg/authz/.
//
// The zero value is intentionally invalid (iota+1, mirroring kernel/saga.Status
// and kernel/outbox.Disposition) so an unset obligation can never be mistaken
// for a permissive scope.
//
// This is a TYPE-ONLY foundation in PR-1; repo methods begin taking RowScope as
// a mandatory positional parameter in PR-4.
type RowScope uint8

const (
	// RowScopeSelf restricts visibility to rows owned by the subject
	// (owner_id = $subject); the non-privileged user default.
	RowScopeSelf RowScope = iota + 1
	// RowScopeDevice restricts visibility to rows belonging to the device
	// subject (device_id = $subject); the principalKind=device default.
	RowScopeDevice
	// RowScopeTenant grants visibility to all rows within the caller's tenant;
	// the admin default.
	RowScopeTenant
	// RowScopeAll grants cross-tenant visibility; reserved for the explicit,
	// audited platform super-admin path only.
	RowScopeAll
)

// Valid reports whether rs is one of the four defined scopes.
func (rs RowScope) Valid() bool {
	return rs >= RowScopeSelf && rs <= RowScopeAll
}

// String returns the wire/log spelling of the scope, or "invalid" for the zero
// value and any out-of-range value.
func (rs RowScope) String() string {
	switch rs {
	case RowScopeSelf:
		return "self"
	case RowScopeDevice:
		return "device"
	case RowScopeTenant:
		return "tenant"
	case RowScopeAll:
		return "all"
	default:
		return "invalid"
	}
}

// Validate returns an error for the zero value or any out-of-range value.
func (rs RowScope) Validate() error {
	if !rs.Valid() {
		return fmt.Errorf("tenant: invalid RowScope %d", rs)
	}
	return nil
}

// ParseRowScope converts a wire/log spelling back to its RowScope value
// (inverse of String). Recognized codes: "self", "device", "tenant", "all".
//
// Empty-string semantics: "" maps to the zero value (RowScope(0)) with no
// error — this is the "no row-scope obligation" sentinel used by the ABAC
// obligations model and the PG codec. Callers that require a non-zero scope
// must check rs.Valid() after parsing.
//
// Any non-empty, unrecognized code returns an error (fail-closed).
func ParseRowScope(s string) (RowScope, error) {
	switch s {
	case "":
		return 0, nil
	case "self":
		return RowScopeSelf, nil
	case "device":
		return RowScopeDevice, nil
	case "tenant":
		return RowScopeTenant, nil
	case "all":
		return RowScopeAll, nil
	default:
		return 0, fmt.Errorf("tenant: unknown RowScope wire code %q", s)
	}
}

// Narrower returns the stricter (narrower-visibility) of rs and other, treating
// the zero value as "no row-scope constraint" (the other operand wins; when both
// are zero, returns zero). It is the single source of the strictness ordering
// self < device < tenant < all: callers (e.g. an ABAC obligation merge that must
// combine multiple permit scopes fail-safely) MUST use this instead of a raw
// numeric `<`, so the cross-package ordering invariant is encoded in exactly one
// place. The ordering is locked by TestRowScope_StrictnessOrdering.
func (rs RowScope) Narrower(other RowScope) RowScope {
	switch {
	case rs == 0:
		return other
	case other == 0:
		return rs
	case rs <= other:
		return rs
	default:
		return other
	}
}
