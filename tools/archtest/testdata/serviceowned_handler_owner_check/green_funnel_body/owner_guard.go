// Package green_funnel_body is a GREEN fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body lock).
// It mirrors the canonical runtime/auth.CheckOwner shape: ownershipMismatch
// helper encapsulates the mismatch predicate, CheckOwner's IF condition
// is exactly a call to that helper, and exactly one
// errcode.New(errcode.KindNotFound, ...) call is the sole non-nil return.
// B2/B2b must stay silent.
package green_funnel_body

import "github.com/ghbvf/gocell/pkg/errcode"

// ownershipMismatch is the canonical helper — body is the operand-identity-
// bound canonical AST tree expected by B2b.
func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner is the canonical funnel form — IF condition is exactly the
// helper call (B2), exactly one errcode.New with KindNotFound is the
// sole non-nil exit (B2 count + return-form), helper body lock applies
// (B2b).
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if ownershipMismatch(resource, ownerID, callerID) {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	return nil
}
