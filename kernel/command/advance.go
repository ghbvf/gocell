package command

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// AdvanceCommand validates a transition and applies the timestamp side effects
// that the kernel owns. This is a kernel-internal helper used by Queue
// implementations; service/application code MUST use Queue methods directly
// (Enqueue/Dequeue/Report/Ack/ExtendLease/Cancel), which internally
// delegate to AdvanceCommand.
//
// Side effects by target status:
//   - Sent:      sets SentAt, increments Attempt
//   - Delivered: sets DeliveredAt
//   - Succeeded/Failed/Expired/Canceled: sets CompletedAt
//   - Pending:   ResetForRetry path; use ResetForRetry directly, not AdvanceCommand
//
// Returns an error if the transition is invalid per statusTransitions, or if
// a required prerequisite timestamp is missing (e.g., Sent→Delivered without SentAt).
func AdvanceCommand(entry *Entry, to Status, now time.Time) error {
	if entry == nil {
		return errcode.New(errcode.ErrValidationFailed, "command: nil Entry")
	}
	// AdvanceCommand does not handle → Pending; ResetForRetry does.
	if to == StatusPending {
		return errcode.New(errcode.ErrValidationFailed,
			"command: AdvanceCommand does not support transitions to Pending; use ResetForRetry")
	}
	if err := Transition(entry.Status, to); err != nil {
		return err
	}

	switch to {
	case StatusSent:
		// Defensive clock-skew check: SentAt should be nil before first Sent
		// transition (ResetForRetry clears it). If SentAt is somehow set already,
		// ensure now does not precede it to prevent backwards timestamps.
		if entry.SentAt != nil && now.Before(*entry.SentAt) {
			return errcode.New(errcode.ErrValidationFailed,
				"command: advance now precedes previous SentAt (clock skew?)")
		}
		entry.SentAt = &now
		entry.Attempt++
	case StatusDelivered:
		if entry.SentAt == nil {
			return errcode.New(errcode.ErrValidationFailed,
				"command: cannot transition to Delivered without SentAt")
		}
		entry.DeliveredAt = &now
	case StatusSucceeded:
		// Succeeded is reachable from Sent (no Delivered) or Delivered.
		// DeliveredAt left nil when transitioning from Sent — signals device
		// skipped the intermediate Report event.
		entry.CompletedAt = &now
	case StatusFailed, StatusExpired, StatusCanceled:
		entry.CompletedAt = &now
	}

	entry.Status = to
	return nil
}

// ResetForRetry resets a command back to Pending for retry. This is the only
// sanctioned way to retry a command — adapters MUST NOT directly mutate
// Status, SentAt, or other fields to simulate retry.
//
// Allowed source states:
//   - StatusSent:   transport delivery failed, retry from scratch
//   - StatusFailed: explicit operator/system retry of a failed command
//
// Disallowed source states:
//   - StatusPending:   already pending (caller bug, no-op not allowed)
//   - StatusDelivered: device ACK'd receipt, resending would duplicate execution
//   - StatusSucceeded/StatusExpired/StatusCanceled: semantically final
//
// Side effects:
//   - Status → StatusPending
//   - SentAt, DeliveredAt, CompletedAt → nil
//   - Attempt is preserved (tracks total attempts across retries)
//   - All other fields (ID, DeviceID, Payload, Timeouts, Metadata, CreatedAt) preserved
func ResetForRetry(entry *Entry) error {
	if entry == nil {
		return errcode.New(errcode.ErrValidationFailed, "command: nil Entry")
	}
	switch entry.Status {
	case StatusSent, StatusFailed:
		// allowed
	default:
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("command: cannot reset for retry from status %s (allowed: sent, failed)", entry.Status))
	}

	entry.Status = StatusPending
	entry.SentAt = nil
	entry.DeliveredAt = nil
	entry.CompletedAt = nil
	return nil
}
