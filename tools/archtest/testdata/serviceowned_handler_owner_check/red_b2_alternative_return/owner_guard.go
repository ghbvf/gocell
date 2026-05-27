// Package red_b2_alternative_return is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B2 (return-form lock):
// CheckOwner contains the canonical errcode.New(KindNotFound, ...) exit
// AND an additional non-canonical bypass return (errors.New). The count
// check passes (notFoundCalls==1), but the return-form walker fires.
package red_b2_alternative_return

import (
	"errors"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func ownershipMismatch[T any](resource T, ownerID func(T) string, callerID string) bool {
	return callerID == "" || ownerID(resource) != callerID
}

// CheckOwner has a non-canonical return path bypassing the IDOR envelope.
func CheckOwner[T any](
	resource T,
	ownerID func(T) string,
	callerID string,
	code errcode.Code,
) error {
	if ownershipMismatch(resource, ownerID, callerID) {
		return errcode.New(errcode.KindNotFound, code, "not found")
	}
	if ownerID(resource) == "DEBUG-LEAK" {
		// BUG: bypass exit — must be the canonical funnel form
		return errors.New("debug-leak bypass")
	}
	return nil
}
