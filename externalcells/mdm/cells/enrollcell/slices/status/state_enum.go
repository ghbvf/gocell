package status

import (
	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"
	statusv1 "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/status/v1"
)

// certStateToEnum maps a certlifecycle.State to the generated ResponseDataStatus
// enum. certlifecycle.State.String() returns the canonical wire string, which is
// byte-identical to the contract enum values:
//
//	StateRequested()  → "requested"
//	StateIssued()     → "issued"
//	StateActive()     → "active"
//	StateNearExpiry() → "near-expiry"
//	StateRenewing()   → "renewing"
//	StateRotated()    → "rotated"
//	StateRevoked()    → "revoked"
//	StateExpired()    → "expired"
//
// (Verified against certlifecycle/state.go stateValue consts and types_gen.go
// ResponseDataStatus consts — all values are identical, no mapping needed.)
func certStateToEnum(s certlifecycle.State) statusv1.ResponseDataStatus {
	switch s {
	case certlifecycle.StateRequested():
		return statusv1.ResponseDataStatusRequested
	case certlifecycle.StateIssued():
		return statusv1.ResponseDataStatusIssued
	case certlifecycle.StateActive():
		return statusv1.ResponseDataStatusActive
	case certlifecycle.StateNearExpiry():
		return statusv1.ResponseDataStatusNearExpiry
	case certlifecycle.StateRenewing():
		return statusv1.ResponseDataStatusRenewing
	case certlifecycle.StateRotated():
		return statusv1.ResponseDataStatusRotated
	case certlifecycle.StateRevoked():
		return statusv1.ResponseDataStatusRevoked
	case certlifecycle.StateExpired():
		return statusv1.ResponseDataStatusExpired
	default:
		// Zero or unknown state: return empty string (zero value for the enum type).
		// The handler will surface this as an empty status field; future callers
		// should use ParseState when hydrating from persistence.
		// NOTE: we do NOT return s.String() here — that would leak internal state
		// representation strings to the wire for unknown values. An empty string is
		// the safe sentinel that callers can detect and handle without exposing
		// implementation details.
		return statusv1.ResponseDataStatus("")
	}
}
