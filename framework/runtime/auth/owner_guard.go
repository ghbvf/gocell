package auth

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// checkOwnerMsg is the const-literal wire message returned by every IDOR
// collapse. The resource type is carried by errcode.Code (e.g.
// ErrSessionNotFound) for programmatic classification; the human-readable
// message is intentionally generic so the funnel stays within
// MESSAGE-CONST-LITERAL-01 (no runtime data on the wire message channel)
// and emits a uniform envelope regardless of which serviceOwned slice
// invoked it.
const checkOwnerMsg = "not found"

// ownershipMismatch is the single source of truth for "what constitutes an
// owner-check failure" in the CheckOwner funnel. Factoring this predicate
// out of CheckOwner's IF condition (a) makes the SERVICEOWNED-HANDLER-
// OWNER-CHECK-01 archtest's B2 condition-lock a one-line callsite check
// (typed function choice Hard form), and (b) localizes future evolution
// of mismatch semantics to this one helper without changing CheckOwner's
// IF shape or archtest predicates.
//
// Returns true when the caller's claim should NOT pass — either the
// callerID is empty (fail-closed defense-in-depth: an upstream auth bug
// that lets an empty subject reach the service layer cannot accidentally
// match a default-zero owner field) OR the ownerID accessor disagrees
// with the callerID.
//
// INVARIANT: SERVICEOWNED-HANDLER-OWNER-CHECK-01 (Hard, B2b)
// CheckOwner's IF.Cond MUST be exactly `ownershipMismatch(...)` and this
// function body MUST be the canonical `callerID == "" || ownerID(resource)
// != callerID` form. Both are archtest-locked.
func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner verifies that callerID matches the owner of resource as
// determined by the ownerID accessor. It returns KindNotFound on mismatch
// or on empty callerID — never PermissionDenied — to avoid leaking
// resource existence to unauthorized callers (IDOR-safe 404 collapse).
//
// Empty callerID is treated as a mismatch (fail-closed): the funnel itself
// rejects empty subjects so that an upstream auth bug (missing JWT subject,
// stale middleware, public-route misconfig) cannot silently grant access.
// The canonical caller pattern (sessionlogout.Service.Logout's empty
// callerUserID guard returning KindInvalid) is still required for proper
// 400 mapping at the handler boundary; CheckOwner's fail-closed check is
// the defense-in-depth backstop.
//
// Accessor is responsible for nil-safety on pointer T: returning "" for a
// nil resource collapses lookup-failure into the same envelope as
// owner-mismatch, preserving the IDOR collapse property.
//
// Details are intentionally absent from the returned envelope: adding
// ownerID or callerID to errcode.Details would leak existence information
// to 4xx clients and break the IDOR-safe collapse contract.
//
// The wire message is the const literal "not found" (see checkOwnerMsg);
// resource-type semantics live in the caller-supplied errcode.Code only.
// This keeps the funnel within MESSAGE-CONST-LITERAL-01 without needing a
// carve-out and makes the IDOR collapse uniform: a session-not-found and
// a device-not-found both surface as `{"code": "<resource-code>",
// "message": "not found"}`.
//
// INVARIANT: SERVICEOWNED-HANDLER-OWNER-CHECK-01 (Hard)
//
// All service-layer ownership checks on serviceOwned contracts must go
// through CheckOwner. service.go in serviceOwned slices must not construct
// errcode.New(errcode.KindNotFound, ...) directly; archtest predicate B3
// enforces this as a zero-tolerance ban.
//
// ref: kubernetes/apiserver pkg/authorization/authorizer/interfaces.go
// (Decision.DenyOnly philosophy); ent privacy/privacy.go (typed sentinel
// for policy decisions).
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if ownershipMismatch(resource, ownerID, callerID) {
		return errcode.New(errcode.KindNotFound, code, checkOwnerMsg)
	}
	return nil
}
