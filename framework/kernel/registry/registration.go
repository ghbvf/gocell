package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ContractRegistration is the projected current state of a runtime-submitted
// contract registration (303-US2, #2233), rebuilt from / kept consistent with
// its append-only RegistrationEvent stream. It carries the registration's
// identity (set at Submit, immutable) plus the mutable projection fields
// (State / Approver / UpdatedAt) advanced by transitions.
//
// All fields are value types (string / sealed RegistrationState / time.Time), so
// a struct copy is a full deep copy — read methods on ContractRegistrar return
// copies, never pointers into the projection (unlike the read-only
// ContractRegistry which hands out pointers to immutable, bootstrap-built data).
type ContractRegistration struct {
	// ID is the registration identifier (Submit dedup key).
	ID string
	// Kind is the contract kind (http / event / command / …), opaque here.
	Kind string
	// PayloadSchema is an opaque schema reference/hash recorded at Submit; the
	// kernel state machine does not interpret it (US5 persists it).
	PayloadSchema string
	// Submitter is the identity that submitted the registration (set at Submit).
	Submitter string
	// Approver is the admin identity that approved the registration, set when the
	// pending-approval → approved transition occurs; empty otherwise.
	Approver string
	// State is the current sealed lifecycle state.
	State RegistrationState
	// CreatedAt is the submit timestamp; UpdatedAt is the last-transition
	// timestamp. Both are clock-stamped inside the registrar lock.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RegistrationEvent is one append-only entry in a registration's migration
// history. The stream is the source of truth for history; the ContractRegistration
// projection is kept consistent with it under the registrar lock. Folding the
// stream from the zero state reproduces the current State (the From of the first
// event is the zero sentinel; each subsequent From chains from the prior To).
type RegistrationEvent struct {
	// RegistrationID is the owning registration's ID. It is injected by the
	// registrar in appendLocked; callers reading via Events() always receive a
	// non-empty value matching the queried id (no need to set it by hand).
	RegistrationID string
	// Seq is the 1-based, contiguous position within this registration's stream.
	Seq int
	// From is the prior state; To is the resulting state. On the initial submit
	// event From is the zero sentinel (From.IsZero() == true), so folding the
	// stream from a zero RegistrationState reproduces the lifecycle.
	From RegistrationState
	To   RegistrationState
	// Actor is the identity driving the transition (submitter / admin / system);
	// Reason is an optional free-form note (e.g. a rejection reason).
	Actor  string
	Reason string
	// OccurredAt is the clock-stamped transition time.
	OccurredAt time.Time
}

// SubmitInput is the data required to create a new registration. PayloadSchema
// is optional; ID, Kind, and Submitter are required.
type SubmitInput struct {
	ID            string
	Kind          string
	PayloadSchema string
	Submitter     string
}

// Normalized returns a copy with the required identity fields trimmed, so padded
// values (" cell-a ") are stored canonically and a whitespace-only field is
// caught by Validate as missing. Optional free-form fields (PayloadSchema) are
// left untouched. Submit normalizes before validating and storing.
//
// Exported (303-US5, #2236) so a durable ports.Registry implementation validates
// and canonicalises submit input identically to the in-mem ContractRegistrar —
// a single validation source, no mem/PG fork.
func (in SubmitInput) Normalized() SubmitInput {
	in.ID = strings.TrimSpace(in.ID)
	in.Kind = strings.TrimSpace(in.Kind)
	in.Submitter = strings.TrimSpace(in.Submitter)
	return in
}

// Validate rejects missing required fields with ErrValidationFailed, treating a
// whitespace-only value as missing (TrimSpace) — a blank ID is a silent map-key
// collision, and a blank Submitter is a phantom audit identity. Exported for
// durable-store reuse (see Normalized).
func (in SubmitInput) Validate() error {
	missing := ""
	switch {
	case strings.TrimSpace(in.ID) == "":
		missing = "id"
	case strings.TrimSpace(in.Kind) == "":
		missing = "kind"
	case strings.TrimSpace(in.Submitter) == "":
		missing = "submitter"
	}
	if missing == "" {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"registry: submit input missing required field",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=%s", missing))))
}

// AdvanceInput is the data required to advance a registration to a new state.
// It mirrors SubmitInput (a named-field struct, not positional args) so that the
// two same-typed strings Actor and Reason cannot be silently swapped at a call
// site. Actor is required (attribution: who drove the transition — submitter /
// admin / system); Reason is optional free-form (e.g. a rejection reason). To's
// legality is enforced by Transition against legalTransitions, not here.
type AdvanceInput struct {
	ID     string
	To     RegistrationState
	Actor  string
	Reason string
}

// Normalized returns a copy with the required identity fields (ID, Actor)
// trimmed so a padded actor is stored canonically and a whitespace-only one is
// caught by Validate. The optional free-form Reason is left untouched. Advance
// normalizes before validating and storing. Exported for durable-store reuse
// (see SubmitInput.Normalized).
func (in AdvanceInput) Normalized() AdvanceInput {
	in.ID = strings.TrimSpace(in.ID)
	in.Actor = strings.TrimSpace(in.Actor)
	return in
}

// Validate rejects missing required fields (ID, Actor) with ErrValidationFailed,
// treating a whitespace-only value as missing (TrimSpace). A non-blank Actor
// closes the audit-attribution gap: an approve / retire transition must name who
// performed it, symmetric with SubmitInput requiring a Submitter. To legality is
// validated by Transition (an illegal/zero target surfaces as
// ErrRegistrationInvalidTransition, not a missing-field error). Exported for
// durable-store reuse (see SubmitInput.Normalized).
func (in AdvanceInput) Validate() error {
	missing := ""
	switch {
	case strings.TrimSpace(in.ID) == "":
		missing = "id"
	case strings.TrimSpace(in.Actor) == "":
		missing = "actor"
	}
	if missing == "" {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"registry: advance input missing required field",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=%s", missing))))
}
