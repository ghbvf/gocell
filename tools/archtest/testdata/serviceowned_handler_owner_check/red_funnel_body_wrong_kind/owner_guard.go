// Package red_funnel_body_wrong_kind is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body lock):
// a hypothetical CheckOwner whose body returns KindPermissionDenied
// instead of KindNotFound — the exact drift B2 must catch.
package red_funnel_body_wrong_kind

import "github.com/ghbvf/gocell/pkg/errcode"

// CheckOwner mimics the runtime/auth.CheckOwner signature but drifts to
// KindPermissionDenied. B2 must report this as a funnel-body violation
// (leaks resource existence — IDOR regression).
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if ownerID(resource) != callerID {
		// BUG: KindPermissionDenied (403) leaks existence — must be KindNotFound (404)
		return errcode.New(errcode.KindPermissionDenied, code, "not found")
	}
	return nil
}
