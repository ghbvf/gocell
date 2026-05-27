// Package red_b2_helper_body_drift is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2b (helper body lock):
// CheckOwner correctly calls ownershipMismatch (condition lock passes),
// but the helper body has the operator flipped to `&&` (instead of `||`).
// Semantic flip: only fails when BOTH empty AND mismatch are true, letting
// any non-empty caller through. B2b catches the AST shape drift.
package red_b2_helper_body_drift

import "github.com/ghbvf/gocell/pkg/errcode"

// BUG: && instead of || — semantic flip, IDOR-unsafe.
func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" && ownerID(resource) != callerID
}

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
