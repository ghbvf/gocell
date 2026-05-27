package saga

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// errDefinitionNotRegistered is returned when the Coordinator's registry has
// no entry for an Instance.DefinitionID. The Coordinator routes this through
// MarkTerminal Failed; callers see KindInvalid + ErrValidationFailed.
func errDefinitionNotRegistered(definitionID idutil.SafeID) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga coordinator: definition not registered",
		errcode.WithDetails(errcode.PublicString("definitionId", string(definitionID))),
	)
}

// errFoldEventMismatch is returned when foldEvents sees a state that the
// state-machine forbids (e.g. KindStepFailed mid-history without a prior
// terminal marker). Defensive — Journal should have MarkTerminal'd already.
//
// instanceID is a legitimate business field exposed in Details; reason is
// a runtime debug string kept server-side via WithInternal (KindInternal
// three-layer rule: runtime debug data must not reach the wire on 5xx).
func errFoldEventMismatch(instanceID idutil.SafeID, reason string) error {
	return errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"saga coordinator: replayed events inconsistent with state machine",
		errcode.WithDetails(
			errcode.PublicString("instanceId", string(instanceID)),
		),
		errcode.WithInternal(errcode.InternalAttr("_", reason)),
	)
}
