// Package red_funnel_body_wrong_kind is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body lock):
// CheckOwner returns KindPermissionDenied instead of KindNotFound.
// ownershipMismatch helper is present and canonical, so B2b stays silent
// — but the otherCalls accumulator in B2 fires on the wrong-Kind exit.
package red_funnel_body_wrong_kind

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// canonical helper — B2b silent.
func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner drifts to KindPermissionDenied.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if ownershipMismatch(resource, ownerID, callerID) {
		// BUG: KindPermissionDenied leaks existence (must be KindNotFound)
		return errcode.New(errcode.KindPermissionDenied, code, "not found")
	}
	return nil
}
