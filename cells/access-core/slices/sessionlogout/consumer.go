package sessionlogout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/cells/access-core/slices/rbacassign"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Consumer handles role-change events and invalidates affected user sessions.
//
// Consumer: cg-access-core-rbac-session-sync
// Topics: event.role.assigned.v1, event.role.revoked.v1
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h,
//
//	key = entry.ID (prefixed "evt-{uuid}" from outbox.Entry)
//
// Ack timing: after sessionRepo.RevokeByUserID returns nil
// Disposition:
//   - unmarshal fail / empty userID  → DispositionReject (PermanentError) → DLX
//   - sessionRepo transient error    → DispositionRequeue → retry with backoff
//   - success                        → DispositionAck
//
// DLX: broker-native via DispositionReject → Nack(requeue=false)
type Consumer struct {
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewConsumer creates a new role-change consumer.
func NewConsumer(repo ports.SessionRepository, logger *slog.Logger) *Consumer {
	return &Consumer{sessionRepo: repo, logger: logger}
}

// HandleRoleChanged is a LegacyHandler (func(context.Context, outbox.Entry) error).
// Compose with outbox.WrapLegacyHandler to obtain an EntryHandler for cell.EventRouter.
//
// Behaviour:
//   - Unmarshal failure → PermanentError (message routed to DLX, no retry).
//   - Empty userId in payload → PermanentError.
//   - sessionRepo error → plain error (transient; WrapLegacyHandler maps to Requeue).
//   - Success → nil (WrapLegacyHandler maps to Ack).
func (c *Consumer) HandleRoleChanged(ctx context.Context, entry outbox.Entry) error {
	var payload rbacassign.RoleChangedEvent
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return outbox.NewPermanentError(
			fmt.Errorf("sessionlogout: decode role-changed payload: %w", err),
		)
	}

	if payload.UserID == "" {
		return outbox.NewPermanentError(
			errcode.New(errcode.ErrAuthRBACInvalidInput, "sessionlogout: role-changed payload missing userId"),
		)
	}

	if err := c.sessionRepo.RevokeByUserID(ctx, payload.UserID); err != nil {
		return fmt.Errorf("sessionlogout: revoke sessions for user %s: %w", payload.UserID, err)
	}

	c.logger.Info("sessions invalidated on role change",
		slog.String("user_id", payload.UserID),
		slog.String("role_id", payload.RoleID),
		slog.String("action", payload.Action),
		slog.String("event_id", entry.ID))

	return nil
}
