package status

import (
	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"
	statusv1 "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/status/v1"
)

// certStateToEnum maps a certlifecycle.State to the generated ResponseDataStatus
// enum. It returns ok=false for the zero / unknown state, which has no valid wire
// enum member — callers MUST fail closed rather than emit it (#2426 F5).
// certlifecycle.State.String() returns the canonical wire string, which is
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
func certStateToEnum(s certlifecycle.State) (statusv1.ResponseDataStatus, bool) {
	switch s {
	case certlifecycle.StateRequested():
		return statusv1.ResponseDataStatusRequested, true
	case certlifecycle.StateIssued():
		return statusv1.ResponseDataStatusIssued, true
	case certlifecycle.StateActive():
		return statusv1.ResponseDataStatusActive, true
	case certlifecycle.StateNearExpiry():
		return statusv1.ResponseDataStatusNearExpiry, true
	case certlifecycle.StateRenewing():
		return statusv1.ResponseDataStatusRenewing, true
	case certlifecycle.StateRotated():
		return statusv1.ResponseDataStatusRotated, true
	case certlifecycle.StateRevoked():
		return statusv1.ResponseDataStatusRevoked, true
	case certlifecycle.StateExpired():
		return statusv1.ResponseDataStatusExpired, true
	default:
		// Zero or unknown state is NOT a valid wire enum: response.schema.json's status
		// enum has no empty member. Return ok=false so the caller fails closed with a
		// framework 5xx rather than emitting a schema-invalid 200 with status:"" (#2426 F5).
		// Returning "" (not s.String()) also keeps the internal state representation off
		// the wire; the empty value is only ever the not-ok sentinel, never serialized.
		return "", false
	}
}
