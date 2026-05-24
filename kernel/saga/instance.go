package saga

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Instance represents a single execution of a saga definition.
//
// The lifecycle: Pending → Running → Succeeded, with Compensating/Compensated
// and Failed/Expired terminals (see [Status]). Status and timestamps are owned
// by the state machine ([NewInstance], [AdvanceSaga], [AdvanceStep]); callers
// MUST NOT mutate them directly.
type Instance struct {
	// ID uniquely identifies this execution (framework-generated).
	ID idutil.SafeID
	// DefinitionID names the saga definition being executed. It is a plain
	// identifier; the SagaDefinition type itself lives with the Coordinator.
	DefinitionID idutil.SafeID

	Status Status
	// CurrentStep is the 0-based forward cursor. It is meaningful only once
	// Status >= Running; AdvanceStep advances it.
	CurrentStep int

	StartedAt   time.Time  // creation instant (caller-provided, not wall-clock)
	UpdatedAt   *time.Time // last transition; nil until the first advance
	CompletedAt *time.Time // set when a terminal status is reached
}

// NewInstance creates an Instance in Pending status. This is the only
// sanctioned constructor; it enforces the creation-time invariants that
// [Instance.ValidateNew] re-asserts:
//   - Status = StatusPending (callers cannot create a non-Pending instance)
//   - CurrentStep = 0
//   - StartedAt = now (caller-provided, making the constructor deterministic)
//   - UpdatedAt / CompletedAt = nil
func NewInstance(id, definitionID idutil.SafeID, now time.Time) Instance {
	return Instance{
		ID:           id,
		DefinitionID: definitionID,
		Status:       StatusPending,
		CurrentStep:  0,
		StartedAt:    now,
	}
}

// ValidateNew checks that an instance is in its initial (just-created) state.
// It is a creation-time validator, NOT suitable for an instance already
// advanced through the lifecycle. It ensures callers cannot bypass the state
// machine by constructing an Instance with an arbitrary status, cursor, or
// timestamps.
func (i *Instance) ValidateNew() error {
	if err := i.validateIdentity(); err != nil {
		return err
	}
	return i.validateInitialState()
}

func (i *Instance) validateIdentity() error {
	if i.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance missing ID")
	}
	if err := i.ID.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance has invalid ID")
	}
	if i.DefinitionID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance missing DefinitionID")
	}
	if err := i.DefinitionID.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance has invalid DefinitionID")
	}
	return nil
}

func (i *Instance) validateInitialState() error {
	if !i.Status.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance has invalid Status")
	}
	if i.Status != StatusPending {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: new instance must have Pending status")
	}
	if i.CurrentStep != 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: new instance must have CurrentStep=0")
	}
	if i.StartedAt.IsZero() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: instance missing StartedAt")
	}
	if i.UpdatedAt != nil || i.CompletedAt != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: new instance must not have UpdatedAt/CompletedAt timestamps")
	}
	return nil
}
