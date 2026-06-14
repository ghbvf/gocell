package saga

import (
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
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

// errFoldUnknownKind is returned when foldEvents encounters a journal.EventKind
// it has no explicit case for — the fail-closed guard for forward-replay (#1950).
// foldEvents enumerates every kind in the journal vocabulary; a value reaching
// this branch means a NEW EventKind was added without deciding how forward
// replay treats it. Silently skipping (the prior `default`) could let a future
// kind re-enter a step incorrectly, so the inconsistency is surfaced rather than
// guessed. TestFoldEvents_AllKindsHandled iterates every Valid() kind, so this
// branch is unreachable for the current vocabulary and trips the moment one is
// added. The kind label is server-side only (WithInternal), per the KindInternal
// three-layer rule.
//
// Carries the dedicated ErrSagaFoldUnknownKind code (not generic ErrInternal) so
// the drive loop logs it at Error severity via foldErrLevel (a code↔schema drift
// is a real correctness anomaly), while the defensive errFoldEventMismatch stays
// Warn. The kind String() is an enum name (e.g. "step_started"), not user data.
func errFoldUnknownKind(kind journal.EventKind) error {
	return errcode.New(errcode.KindInternal, errcode.ErrSagaFoldUnknownKind,
		"saga coordinator: foldEvents saw an unhandled journal event kind",
		errcode.WithInternal(errcode.InternalAttr("_", kind.String())),
	)
}
