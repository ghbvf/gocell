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
// place rather than being re-typed at each call site (memjournal returns
// errInstanceNotFound from four methods). Callers discriminate by errcode.Code,
// not by helper identity, so a sibling implementation (PR-04 PG store) reaching
// the same code yields the same caller-observable error class.

// errInstanceNotFound reports that the addressed saga instance was never
// enqueued. Returned by Load / Append / Heartbeat / MarkTerminal.
func errInstanceNotFound(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrSagaNotFound,
		"saga journal: instance not found",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// errDuplicateInstance reports that Enqueue was called for an instance ID that
// already exists.
func errDuplicateInstance(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaDuplicateInstance,
		"saga journal: instance already enqueued",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// errStaleLease reports that a lease-fenced Append presented a leaseID that no
// longer owns the instance (lost / expired / superseded). A zombie leader must
// not corrupt the event stream, so Append surfaces this rather than dropping
// the write silently.
func errStaleLease(instanceID, leaseID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaStaleLease,
		"saga journal: stale lease on append",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s leaseID=%s", instanceID, leaseID)),
	)
}

// errInvalidTerminalStatus reports that MarkTerminal was given a non-terminal
// status, or one the instance's current status cannot legally transition to.
func errInvalidTerminalStatus(instanceID idutil.SafeID, from, to saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: invalid terminal status",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s from=%s to=%s", instanceID, from, to)),
	)
}

// errNonPositiveBatchSize reports that ClaimPending received a batchSize ≤ 0.
func errNonPositiveBatchSize(batchSize int) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: ClaimPending batchSize must be positive",
		errcode.WithInternal(fmt.Sprintf("batchSize=%d", batchSize)),
	)
}

// errNonPositiveLeaseDuration reports that ClaimPending / Heartbeat received a
// leaseDuration ≤ 0 (which would mint an immediately-expired or boundary lease).
func errNonPositiveLeaseDuration(d time.Duration) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: leaseDuration must be positive",
		errcode.WithInternal(fmt.Sprintf("leaseDuration=%s", d)),
	)
}

// errEventPhase reports that an event kind was appended in a status that does
// not permit it (e.g. a forward step while Compensating, or KindStepCompensated
// while not Compensating). The projection is a fold of the event log, so an
// out-of-phase event would desynchronise status from history.
func errEventPhase(instanceID idutil.SafeID, kind EventKind, status saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: event kind not allowed in current phase",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s kind=%s status=%s", instanceID, kind, status)),
	)
}
