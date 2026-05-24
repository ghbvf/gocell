package journal

import (
	"fmt"

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
//
// Sentinel reuse, registered for upgrade: there is no generic not-found Code in
// pkg/errcode, so instance-not-found pairs KindNotFound with ErrValidationFailed
// for now. PR-04/PR-08 introduce dedicated ErrSagaNotFound / ErrSagaStaleLease /
// ErrSagaDuplicateInstance codes for operator routing (see gh issue #956
// (PR-08 dedicated ErrSaga* codes)). A kernel-feature PR deliberately does not
// edit pkg/errcode (which would trip the contract-fanout errcode-sentinel scan).

// errInstanceNotFound reports that the addressed saga instance was never
// enqueued. Returned by Load / Append / Heartbeat / MarkTerminal.
func errInstanceNotFound(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrValidationFailed,
		"saga journal: instance not found",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// errDuplicateInstance reports that Enqueue was called for an instance ID that
// already exists.
func errDuplicateInstance(instanceID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrConflict,
		"saga journal: instance already enqueued",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", instanceID)),
	)
}

// errStaleLease reports that a lease-fenced Append presented a leaseID that no
// longer owns the instance (lost / expired / superseded). A zombie leader must
// not corrupt the event stream, so Append surfaces this rather than dropping
// the write silently.
func errStaleLease(instanceID, leaseID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrConflict,
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
