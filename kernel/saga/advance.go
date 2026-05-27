package saga

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// AdvanceSaga validates a status transition and applies the timestamp side
// effects that the kernel owns. It is fail-closed: an illegal transition or a
// clock regression returns an error WITHOUT mutating the instance.
//
// Side effects on success:
//   - every transition sets UpdatedAt = now
//   - a terminal target additionally sets CompletedAt = now
//
// Time is injected via now; the state machine never reads the wall clock.
func AdvanceSaga(inst *Instance, to Status, now time.Time) error {
	if inst == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: nil Instance")
	}
	if err := Transition(inst.Status, to); err != nil {
		return err
	}
	if err := guardMonotonic(inst, now); err != nil {
		return err
	}

	inst.UpdatedAt = &now
	if to.IsTerminal() {
		inst.CompletedAt = &now
	}
	inst.Status = to
	return nil
}

// AdvanceStep advances the forward step cursor by one within a Running saga.
// stepCount is the total number of steps in the definition (the Coordinator
// passes def.Len()); this keeps AdvanceStep self-contained and free of any
// dependency on the SagaDefinition type. Fail-closed: rejects when the saga is
// not Running, when stepCount is not positive, when CurrentStep is out of the
// valid cursor range [0, stepCount-1), or on clock regression. The range check
// is the guardian of CurrentStep for replayed/loaded instances (PR-02/PR-04),
// not just freshly constructed ones.
//
// AdvanceStep is NOT used for the final step: once CurrentStep reaches
// stepCount-1, the Coordinator instead transitions Running → Succeeded via
// AdvanceSaga.
func AdvanceStep(inst *Instance, stepCount int, now time.Time) error {
	if inst == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "saga: nil Instance")
	}
	if inst.Status != StatusRunning {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: AdvanceStep requires Running status",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("status=%s", inst.Status))))
	}
	if stepCount <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: AdvanceStep requires positive stepCount",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("stepCount=%d", stepCount))))
	}
	if inst.CurrentStep < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: AdvanceStep on instance with negative CurrentStep",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("currentStep=%d", inst.CurrentStep))))
	}
	// Compared as CurrentStep >= stepCount-1 (not CurrentStep+1 >= stepCount) to
	// stay overflow-safe: a corrupt/replayed CurrentStep near math.MaxInt would
	// otherwise wrap negative under +1 and slip past the bound. stepCount is
	// guaranteed positive above, so stepCount-1 never underflows.
	if inst.CurrentStep >= stepCount-1 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: AdvanceStep would exceed step count",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("currentStep=%d stepCount=%d", inst.CurrentStep, stepCount))))
	}
	if err := guardMonotonic(inst, now); err != nil {
		return err
	}

	inst.CurrentStep++
	inst.UpdatedAt = &now
	return nil
}

// guardMonotonic rejects a now that runs backwards relative to the instance's
// known timestamps, preventing clock-skew from producing non-monotonic history.
// Equality is allowed (non-strict monotonicity): now == StartedAt or
// now == UpdatedAt passes, because same-instant transitions are legal and the
// Journal (PR-02) orders events by sequence, not by timestamp uniqueness.
func guardMonotonic(inst *Instance, now time.Time) error {
	if now.Before(inst.StartedAt) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: advance now precedes StartedAt (clock skew?)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("now=%s startedAt=%s", now, inst.StartedAt))))
	}
	if inst.UpdatedAt != nil && now.Before(*inst.UpdatedAt) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga: advance now precedes previous UpdatedAt (clock skew?)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("now=%s updatedAt=%s", now, *inst.UpdatedAt))))
	}
	return nil
}
