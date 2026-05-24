package journal

import "github.com/ghbvf/gocell/pkg/errcode"

// This file centralises the operation-level errors a Journal implementation
// returns, so the const-literal messages and (kind, code) pairings live in one
// place rather than being re-typed at each call site (memjournal returns
// errInstanceNotFound from four methods). Callers discriminate by errcode.Code,
// not by helper identity, so a sibling implementation (PR-04 PG store) reaching
// the same code yields the same caller-observable error class.
//
// Sentinel reuse, registered for upgrade: there is no generic not-found Code in
// pkg/errcode, so instance-not-found pairs KindNotFound with ErrValidationFailed
// for now. PR-04/PR-08 introduce dedicated ErrSagaNotFound / ErrSagaStaleLease /
// ErrSagaDuplicateInstance codes for operator routing (see the saga plan's PR-08
// archtest backlog). A kernel-feature PR deliberately does not edit pkg/errcode
// (which would trip the contract-fanout errcode-sentinel scan).

// errInstanceNotFound reports that the addressed saga instance was never
// enqueued. Returned by Load / Append / Heartbeat / MarkTerminal.
func errInstanceNotFound() error {
	return errcode.New(errcode.KindNotFound, errcode.ErrValidationFailed,
		"saga journal: instance not found")
}

// errDuplicateInstance reports that Enqueue was called for an instance ID that
// already exists.
func errDuplicateInstance() error {
	return errcode.New(errcode.KindConflict, errcode.ErrConflict,
		"saga journal: instance already enqueued")
}

// errStaleLease reports that a lease-fenced Append presented a leaseID that no
// longer owns the instance (lost / expired / superseded). A zombie leader must
// not corrupt the event stream, so Append surfaces this rather than dropping
// the write silently.
func errStaleLease() error {
	return errcode.New(errcode.KindConflict, errcode.ErrConflict,
		"saga journal: stale lease on append")
}

// errInvalidTerminalStatus reports that MarkTerminal was given a non-terminal
// status, or one the instance's current status cannot legally transition to.
func errInvalidTerminalStatus() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: invalid terminal status")
}
