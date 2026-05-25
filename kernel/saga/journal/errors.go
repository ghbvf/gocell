package journal

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// This file centralizes the operation-level errors a Journal implementation
// returns, so the const-literal messages and (kind, code) pairings live in one
// place. Functions are exported so sibling implementations (adapters/postgres
// /saga.PGJournal in PR-04, future impls thereafter) reuse them directly
// rather than duplicating the constructors. Callers discriminate by
// errcode.Code, not by helper identity.

// NewInstanceNotFoundError reports that the addressed saga instance was never
// enqueued. Returned by Load / Append / Heartbeat / MarkTerminal.
func NewInstanceNotFoundError(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrSagaNotFound,
		"saga journal: instance not found",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// NewDuplicateInstanceError reports that Enqueue was called for an instance
// ID that already exists.
func NewDuplicateInstanceError(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaDuplicateInstance,
		"saga journal: instance already enqueued",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// NewStaleLeaseError reports that a lease-fenced Append presented a leaseID
// that no longer owns the instance (lost / expired / superseded). A zombie
// leader must not corrupt the event stream, so Append surfaces this rather
// than dropping the write silently.
func NewStaleLeaseError(instanceID, leaseID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaStaleLease,
		"saga journal: stale lease on append",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s leaseID=%s", instanceID, leaseID)),
	)
}

// NewInvalidTerminalStatusError reports that MarkTerminal was given a
// non-terminal status, or one the instance's current status cannot legally
// transition to.
func NewInvalidTerminalStatusError(instanceID idutil.SafeID, from, to saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: invalid terminal status",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s from=%s to=%s", instanceID, from, to)),
	)
}

// NewNonPositiveBatchSizeError reports that ClaimPending received a
// batchSize ≤ 0.
func NewNonPositiveBatchSizeError(batchSize int) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: ClaimPending batchSize must be positive",
		errcode.WithInternal(fmt.Sprintf("batchSize=%d", batchSize)),
	)
}

// NewNonPositiveLeaseDurationError reports that ClaimPending / Heartbeat
// received a leaseDuration ≤ 0 (which would mint an immediately-expired or
// boundary lease).
func NewNonPositiveLeaseDurationError(d time.Duration) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: leaseDuration must be positive",
		errcode.WithInternal(fmt.Sprintf("leaseDuration=%s", d)),
	)
}

// NewEventPhaseError reports that an event kind was appended in a status that
// does not permit it (e.g. a forward step while Compensating, or
// KindStepCompensated while not Compensating). The projection is a fold of
// the event log, so an out-of-phase event would desynchronise status from
// history.
func NewEventPhaseError(instanceID idutil.SafeID, kind EventKind, status saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: event kind not allowed in current phase",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s kind=%s status=%s", instanceID, kind, status)),
	)
}

// ─── package-private aliases for backward compatibility within journal/ ───
// memjournal.go uses the lowercase names; alias rather than rewrite to keep
// the rename surgical.

func errInstanceNotFound(id idutil.SafeID) error {
	return NewInstanceNotFoundError(id)
}

func errDuplicateInstance(id idutil.SafeID) error {
	return NewDuplicateInstanceError(id)
}

func errStaleLease(instanceID, leaseID idutil.SafeID) error {
	return NewStaleLeaseError(instanceID, leaseID)
}

func errInvalidTerminalStatus(id idutil.SafeID, from, to saga.Status) error {
	return NewInvalidTerminalStatusError(id, from, to)
}

func errNonPositiveBatchSize(n int) error {
	return NewNonPositiveBatchSizeError(n)
}

func errNonPositiveLeaseDuration(d time.Duration) error {
	return NewNonPositiveLeaseDurationError(d)
}

func errEventPhase(id idutil.SafeID, kind EventKind, status saga.Status) error {
	return NewEventPhaseError(id, kind, status)
}
