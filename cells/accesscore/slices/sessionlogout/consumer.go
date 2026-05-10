package sessionlogout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Consumer handles role-change events and invalidates affected user sessions.
//
// Consumer: cg-accesscore-rbac-session-sync
// Topics: event.role.assigned.v1, event.role.revoked.v1
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h,
//
//	key = entry.ID (prefixed "evt-{uuid}" from outbox.Entry)
//
// Wiring: these guarantees are provided by outbox.ConsumerBase, which
// bootstrap injects via WithConsumerMiddleware — the handler below only
// needs to produce a HandleResult; ConsumerBase wraps it to
// enforce claim/backoff/DLX semantics. See cmd/corebundle/main.go for the
// concrete wiring (in-mem Claimer in corebundle; redis IdempotencyClaimer
// in multi-pod deployments).
//
// Ack timing: after sessionRepo.RevokeByUserID returns nil
// Disposition:
//   - unmarshal fail / empty userID  → DispositionReject (PermanentError) → DLX
//   - sessionRepo transient error    → DispositionRequeue → retry with backoff
//   - success                        → DispositionAck
//
// DLX: broker-native via DispositionReject → Nack(requeue=false).
type Consumer struct {
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewConsumer creates a new role-change consumer.
func NewConsumer(repo ports.SessionRepository, logger *slog.Logger) *Consumer {
	return &Consumer{sessionRepo: repo, logger: logger}
}

// HandleRoleChanged is an EntryHandler (func(context.Context, outbox.Entry) outbox.HandleResult).
// Register directly via reg.Subscribe — no WrapLegacyHandler needed.
//
// Behavior:
//   - Unmarshal failure → DispositionReject (PermanentError, routed to DLX).
//   - Empty userId in payload → DispositionReject (PermanentError, routed to DLX).
//   - sessionRepo error → DispositionRequeue (transient, retried by ConsumerBase).
//   - Success → DispositionAck.
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

	if err := c.sessionRepo.RevokeByUserID(ctx, payload.UserID); err != nil {
		c.logger.Warn("sessionlogout: revoke sessions failed",
			slog.String("user_id", payload.UserID),
			slog.Any("error", err))
		return outbox.Requeue(fmt.Errorf("sessionlogout: revoke sessions: %w", err))
	}

	c.logger.Info("sessions invalidated on role change",
		slog.String("user_id", payload.UserID),
		slog.String("role_id", payload.RoleID),
		slog.String("action", payload.Action),
		slog.String("event_id", entry.ID))

	return outbox.Ack()
}
