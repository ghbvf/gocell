package sessionlogout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Consumer handles role-change events.
//
// PRIMARY credential invalidation (BumpAuthzEpoch + RevokeForSubject +
// RevokeUser) is performed atomically by rbacassign.Service.Revoke via the
// credentialinvalidate funnel in the same transaction as the role removal. This
// consumer processes the downstream outbox fact for observability and audit
// purposes — it does NOT call RevokeForSubject again.
//
// HIGH-3: role assignment (ActionAssigned) is additive and does not invalidate
// credentials. This consumer Ack's assigned events without further action.
// Unknown action values are Rejected permanently to DLX to prevent silent
// data loss on future protocol extensions.
//
// Consumer: cg-accesscore-rbac-session-sync
// Topics: event.role.assigned.v1, event.role.revoked.v1
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h,
//
//	key = entry.ID (prefixed "evt-{uuid}" from outbox.Entry)
//
// Disposition:
//   - unmarshal fail / empty userID → DispositionReject (PermanentError) → DLX
//   - unknown action               → DispositionReject (PermanentError) → DLX
//   - assigned                     → DispositionAck (no-op, funnel already ran)
//   - revoked                      → DispositionAck (funnel ran in rbacassign tx)
//
// DLX: broker-native via DispositionReject → Nack(requeue=false).
type Consumer struct {
	logger *slog.Logger
}

// NewConsumer creates a new role-change consumer.
func NewConsumer(logger *slog.Logger) *Consumer {
	return &Consumer{logger: logger}
}

// HandleRoleChanged is an EntryHandler (func(context.Context, outbox.Entry) outbox.HandleResult).
// Register directly via reg.Subscribe — no WrapLegacyHandler needed.
//
// Behavior:
//   - Unmarshal failure  → DispositionReject (PermanentError, routed to DLX).
//   - Empty userId       → DispositionReject (PermanentError, routed to DLX).
//   - Unknown action     → DispositionReject (PermanentError, routed to DLX).
//   - ActionAssigned     → DispositionAck (additive, no credential invalidation).
//   - ActionRevoked      → DispositionAck (funnel already ran in rbacassign tx).
func (c *Consumer) HandleRoleChanged(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	var payload dto.RoleChangedEvent
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("sessionlogout: decode role-changed payload: %w", err)))
	}

	if payload.UserID == "" {
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrAuthRBACInvalidInput, "sessionlogout: role-changed payload missing userId"),
		))
	}

	switch payload.Action {
	case dto.ActionAssigned:
		// HIGH-3: assignment is additive; no credential invalidation needed.
		c.logger.Info("role assigned event received — no credential invalidation",
			slog.String("user_id", payload.UserID),
			slog.String("role_id", payload.RoleID),
			slog.String("event_id", entry.ID))
	case dto.ActionRevoked:
		// Credential invalidation already performed by rbacassign.Revoke
		// via the credentialinvalidate funnel in the same transaction.
		c.logger.Info("role revoked event received — credential invalidation already applied",
			slog.String("user_id", payload.UserID),
			slog.String("role_id", payload.RoleID),
			slog.String("event_id", entry.ID))
	default:
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("sessionlogout: unknown role-changed action %q for user %s", payload.Action, payload.UserID),
		))
	}

	return outbox.Ack()
}
