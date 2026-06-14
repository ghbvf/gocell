// Package red_b2_helper_unused is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (condition lock):
// the ownershipMismatch helper IS declared with the canonical body, but
// CheckOwner inlines the mismatch predicate instead of calling the helper.
// B2b stays silent (helper is canonical) but the condition lock fires
// (typed function choice form violated).
package red_b2_helper_unused

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// Canonical helper — B2b passes — but unused by CheckOwner below.
//
//nolint:unused // intentional fixture
func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner inlines the mismatch predicate instead of calling the helper.
// B2 condition lock fires: IF condition is not a call to ownershipMismatch.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	// BUG: inline predicate instead of helper call — bypasses single-source-of-truth
	if callerID == "" || ownerID(resource) != callerID {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	return nil
}
