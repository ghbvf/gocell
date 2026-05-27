// Package red_b2_alternative_return is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (funnel body return-form
// lock): CheckOwner contains the canonical errcode.New(KindNotFound, ...)
// exit AND an additional non-canonical return path (returning a
// freshly-constructed errors.New error). Count-only B2 would pass with
// notFoundCalls==1; the return-form walker catches the bypass.
package red_b2_alternative_return

import (
	"errors"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CheckOwner mimics the runtime/auth.CheckOwner signature but has a second
// non-nil exit through errors.New that bypasses the IDOR-safe envelope.
// B2 must report this as a return-form violation.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if callerID == "" || ownerID(resource) != callerID {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	if ownerID(resource) == "DEBUG-LEAK" {
		// BUG: bypass return path — must use canonical errcode.New(KindNotFound, ...)
		return errors.New("debug-leak bypass")
	}
	return nil
}
