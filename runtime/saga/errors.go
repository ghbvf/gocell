package saga

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// errCoordinatorOp is the const-literal operation tag for errcode.New calls
// in this package. It MUST be a const literal (not a runtime-derived string)
// per error-handling rule MESSAGE-CONST-LITERAL-01.
//
//nolint:unused // consumed by Coordinator methods added in the next commit (Batch 2)
const errCoordinatorOp = "runtime/saga.Coordinator"

// errDefinitionNotRegistered is returned when the Coordinator's registry has
// no entry for an Instance.DefinitionID. The Coordinator routes this through
// MarkTerminal Failed; callers see KindInvalid + ErrValidationFailed.
func errDefinitionNotRegistered(definitionID idutil.SafeID) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga coordinator: definition not registered",
		errcode.WithDetails(slog.String("definitionId", string(definitionID))),
	)
}

// errFoldEventMismatch is returned when foldEvents sees a state that the
// state-machine forbids (e.g. KindStepFailed mid-history without a prior
// terminal marker). Defensive — Journal should have MarkTerminal'd already.
func errFoldEventMismatch(instanceID idutil.SafeID, reason string) error {
	return errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"saga coordinator: replayed events inconsistent with state machine",
		errcode.WithDetails(
			slog.String("instanceId", string(instanceID)),
			slog.String("reason", reason),
		),
	)
}
