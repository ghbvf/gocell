package command

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// AdvanceCommand validates a transition and applies the timestamp side effects
// that the kernel owns. This is the canonical entry point for all state changes.
// Adapters MUST call this before persisting the new status.
//
// Side effects by target status:
//   - Sent:      sets SentAt, increments Attempt
//   - Delivered: sets DeliveredAt
//   - Succeeded/Failed/Expired/Canceled: sets CompletedAt
//
// Returns an error if the transition is invalid or if a required prerequisite
// timestamp is missing (e.g., transitioning to Delivered without SentAt).
func AdvanceCommand(entry *Entry, to Status, now time.Time) error {
	if entry == nil {
		return errcode.New(errcode.ErrValidationFailed, "command: nil Entry")
	}
	if err := Transition(entry.Status, to); err != nil {
		return err
	}

	switch to {
	case StatusSent:
		entry.SentAt = &now
		entry.Attempt++
	case StatusDelivered:
		if entry.SentAt == nil {
			return errcode.New(errcode.ErrValidationFailed,
				"command: cannot transition to Delivered without SentAt")
		}
		entry.DeliveredAt = &now
	case StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled:
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
