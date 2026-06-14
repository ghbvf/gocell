// Package red_funnel_body_extra_new is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (count uniqueness):
// the CheckOwner body contains two errcode.New(KindNotFound, ...) calls.
// ownershipMismatch helper is canonical (B2b silent), but the count check
// fires because notFoundCalls != 1.
package red_funnel_body_extra_new

import "github.com/ghbvf/gocell/framework/pkg/errcode"

func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner has two exits, obscuring the single sanctioned funnel form.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if callerID == "" {
		// BUG: extra exit defeats single-form uniqueness
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	if ownershipMismatch(resource, ownerID, callerID) {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	return nil
}
