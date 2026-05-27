package auth

import "github.com/ghbvf/gocell/pkg/errcode"

// CheckOwner verifies that callerID matches the owner of resource as
// determined by the ownerID accessor. It returns KindNotFound on mismatch —
// never PermissionDenied — to avoid leaking resource existence to
// unauthorized callers (IDOR-safe 404 collapse).
//
// Accessor is responsible for nil-safety on pointer T: returning "" for a
// nil resource collapses lookup-failure into the same envelope as
// owner-mismatch, preserving the IDOR collapse property. Callers must
// pre-validate callerID is non-empty (e.g., reject empty subjects at the
// HTTP handler boundary with KindInvalid) so the accessor's "" cannot
// accidentally match.
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
	msg string,
) error {
	if ownerID(resource) != callerID {
		return errcode.New(errcode.KindNotFound, code, msg)
	}
	return nil
}
