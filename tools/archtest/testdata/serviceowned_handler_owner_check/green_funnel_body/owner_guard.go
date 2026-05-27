// Package green_funnel_body is a GREEN fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body lock).
// It mirrors the canonical runtime/auth.CheckOwner shape: exactly one
// errcode.New(errcode.KindNotFound, ...) call, no other Kind. B2 must
// stay silent on this fixture.
package green_funnel_body

import "github.com/ghbvf/gocell/pkg/errcode"

// CheckOwner is the canonical funnel form — single exit on fail-closed
// (empty callerID or owner mismatch), no other errcode.New calls. The
// wire message is a const literal so the funnel stays within
// MESSAGE-CONST-LITERAL-01.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if callerID == "" || ownerID(resource) != callerID {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	return nil
}
