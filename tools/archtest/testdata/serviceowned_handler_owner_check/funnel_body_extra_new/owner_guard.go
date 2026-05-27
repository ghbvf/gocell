// Package funnel_body_extra_new is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body lock):
// the CheckOwner body contains TWO errcode.New(KindNotFound, ...) calls.
// B2 requires exactly one — multiple exits obscure the single sanctioned
// funnel form.
package funnel_body_extra_new

import "github.com/ghbvf/gocell/pkg/errcode"

// CheckOwner mimics the runtime/auth.CheckOwner signature but constructs
// the KindNotFound envelope twice. B2 must report count != 1.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
	msg string,
) error {
	if callerID == "" {
		// BUG: an extra exit obscures the sole funnel form (B2 violation)
		return errcode.New(errcode.KindNotFound, code, msg)
	}
	if ownerID(resource) != callerID {
		return errcode.New(errcode.KindNotFound, code, msg)
	}
	return nil
}
