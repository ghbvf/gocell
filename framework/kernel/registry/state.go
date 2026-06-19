package registry

// RegistrationState is the sealed lifecycle state of a runtime-submitted
// contract registration (303-US2, #2233). A non-zero RegistrationState cannot
// be constructed outside this package — the single field is unexported and the
// only values are the package-private singletons exposed via the accessor
// functions below (reassigning a function is a compile error). This makes
// "an external package cannot forge a state or jump the state machine"
// expressible at compile time (AI-robust Hard), mirroring
// runtime/transport.TransportMode, kernel/metadata.ContractOwner, and
// pkg/authz.Permission.
//
// The zero value is invalid: String() renders it as [RegistrationStateUnknown]
// (fail-closed), never the empty string, and it is not a member of
// allRegistrationStates, so the transition table never accepts it as a source
// or target.
type RegistrationState struct {
	// v is the wire/label value ("submitted" | "probing" | …). Unexported: a
	// non-zero RegistrationState cannot be constructed outside this package.
	v string
}

// RegistrationStateUnknown is the fail-closed render of a zero-value (forged)
// RegistrationState. It is NOT a producible state (not in allRegistrationStates).
// It is a plain string (the String() output), not a RegistrationState value — to
// test for a forged/uninitialised state use IsZero(), not a string comparison.
const RegistrationStateUnknown = "unknown"

// String returns the wire/label value. The zero value renders as
// [RegistrationStateUnknown] (fail-closed), never the empty string.
func (s RegistrationState) String() string {
	if s.v == "" {
		return RegistrationStateUnknown
	}
	return s.v
}

// IsZero reports whether s is the zero (forged/uninitialised) value.
func (s RegistrationState) IsZero() bool { return s.v == "" }

// Package-private singletons — the sole RegistrationState values. Exposed via
// accessor functions (not exported vars) so the registered values are
// immutable: reassigning a function is a compile error.
var (
	stateSubmitted       = RegistrationState{v: "submitted"}
	stateProbing         = RegistrationState{v: "probing"}
	stateConformant      = RegistrationState{v: "conformant"}
	statePendingApproval = RegistrationState{v: "pending-approval"}
	stateApproved        = RegistrationState{v: "approved"}
	stateRejected        = RegistrationState{v: "rejected"}
	stateActive          = RegistrationState{v: "active"}
	stateRetired         = RegistrationState{v: "retired"}
)

// StateSubmitted: a contract registration was accepted by the gate and recorded.
func StateSubmitted() RegistrationState { return stateSubmitted }

// StateProbing: automated conformance probing is in progress.
func StateProbing() RegistrationState { return stateProbing }

// StateConformant: conformance probing passed; awaiting admin review.
func StateConformant() RegistrationState { return stateConformant }

// StatePendingApproval: queued for an admin approve/reject decision.
func StatePendingApproval() RegistrationState { return statePendingApproval }

// StateApproved: an admin approved the registration; it may be activated.
func StateApproved() RegistrationState { return stateApproved }

// StateRejected: conformance failed or an admin rejected it (terminal — never
// activatable).
func StateRejected() RegistrationState { return stateRejected }

// StateActive: the contract is live on the data plane (reachable only from
// approved).
func StateActive() RegistrationState { return stateActive }

// StateRetired: an admin retired a previously active contract (terminal).
func StateRetired() RegistrationState { return stateRetired }

// allRegistrationStates is the closed registry of every RegistrationState value.
// It backs the anti-vacuity freeze (TestRegistrationState_FrozenRegistry): a new
// state added without registering it here — or a renamed wire value — is caught.
var allRegistrationStates = []RegistrationState{
	stateSubmitted, stateProbing, stateConformant, statePendingApproval,
	stateApproved, stateRejected, stateActive, stateRetired,
}

// isRegistered reports whether s is one of the producible states. A zero value
// (forged) is NOT registered, so the transition table fail-closes on it.
func (s RegistrationState) isRegistered() bool {
	for _, x := range allRegistrationStates {
		if x == s {
			return true
		}
	}
	return false
}

// IsTerminal reports whether s is a terminal (final) state with no outgoing
// transitions: rejected (conformance failed / admin rejected) or retired.
func (s RegistrationState) IsTerminal() bool {
	switch s {
	case stateRejected, stateRetired:
		return true
	default:
		return false
	}
}

// StateNames returns the ordered wire/label values for every producible
// RegistrationState. The slice is sorted in lifecycle order and is the canonical
// single source for "what strings are valid?" — used by validation helpers to
// populate the allowed-values detail in structured error responses without
// hard-coding the list in call sites.
//
// The returned slice is a fresh copy each call; callers must not mutate it.
func StateNames() []string {
	names := make([]string, len(allRegistrationStates))
	for i, s := range allRegistrationStates {
		names[i] = s.v
	}
	return names
}

// ParseState resolves a wire/label string back to its sealed RegistrationState —
// the inverse of String() and the sole way a durable store reconstructs a state
// read from its `state TEXT` column without forging one (the unexported field
// makes a non-zero RegistrationState unconstructable outside this package). The
// empty string maps to the zero sentinel (ok=true): it is the From of an initial
// submit event, persisted as an empty string so folding from empty reproduces the
// lifecycle. Any other unrecognized label — including [RegistrationStateUnknown]
// ("unknown"), the fail-closed render that is never a persisted state — returns
// (zero, false).
//
// Added 303-US5 (#2236) for the PostgreSQL ports.Registry implementation.
func ParseState(s string) (RegistrationState, bool) {
	if s == "" {
		return RegistrationState{}, true // zero sentinel — initial-event From
	}
	for _, st := range allRegistrationStates {
		if st.v == s {
			return st, true
		}
	}
	return RegistrationState{}, false
}
