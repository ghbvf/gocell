package registry

import (
	"fmt"
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
	// RegistrationID is the owning registration's ID.
	RegistrationID string
	// Seq is the 1-based, contiguous position within this registration's stream.
	Seq int
	// From is the prior state (the zero sentinel for the initial submit event);
	// To is the resulting state.
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

// validate rejects empty required fields with ErrValidationFailed. An empty ID
// is especially hazardous (a "" map key is a silent collision), so it is barred.
func (in SubmitInput) validate() error {
	missing := ""
	switch {
	case in.ID == "":
		missing = "id"
	case in.Kind == "":
		missing = "kind"
	case in.Submitter == "":
		missing = "submitter"
	}
	if missing == "" {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"registry: submit input missing required field",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=%s", missing))))
}
