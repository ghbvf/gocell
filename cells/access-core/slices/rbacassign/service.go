package rbacassign

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

// Service handles RBAC role assignment and revocation.
//
// Durable mode (outboxWriter + txRunner both non-nil): writes role row and
// event.role.{assigned,revoked}.v1 outbox entry atomically in one DB transaction.
// Session revocation is handled asynchronously by sessionlogout.Consumer which
// subscribes to both topics. This provides L2 OutboxFact atomicity — the partial-commit
// window from the legacy dual-write is eliminated.
//
// Demo mode (both nil): preserves today's synchronous dual-write behaviour —
// roleRepo.{AssignToUser,RemoveFromUserIfNotLast} then sessionRepo.RevokeByUserID
// in-process. Suitable for in-memory repos where "transaction" semantics are
// implicit within a single goroutine.
//
// ref: Watermill SQL outbox + sessionlogin/service.go persistSession pattern.
type Service struct {
	roleRepo     ports.RoleRepository
	sessionRepo  ports.SessionRepository
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// Option configures a rbac-assign Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing (L2 durable mode).
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for L2 atomicity (must be paired with WithOutboxWriter).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// NewService creates a new rbac-assign service.
// In durable mode (both WithOutboxWriter and WithTxManager provided), session
// revocation is delegated to the sessionlogout consumer via the outbox.
// In demo mode (no opts), the legacy dual-write is used.
func NewService(
	roleRepo ports.RoleRepository,
	sessionRepo ports.SessionRepository,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		roleRepo:    roleRepo,
		sessionRepo: sessionRepo,
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// isDurableMode returns true when both outboxWriter and txRunner are configured.
func (s *Service) isDurableMode() bool {
	return s.outboxWriter != nil && s.txRunner != nil
}

// runInTx wraps fn in a transaction if txRunner is configured, otherwise calls fn directly.
func (s *Service) runInTx(ctx context.Context, fn func(context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

// writeOutboxEntry writes a role-change outbox entry. Called inside a transaction.
func (s *Service) writeOutboxEntry(ctx context.Context, eventType string, evt RoleChangedEvent) error {
	if s.outboxWriter == nil {
		return nil
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rbac-assign: marshal role-changed event: %w", err)
	}
	entry := outbox.Entry{
		ID:        "evt" + "-" + uuid.NewString(),
		EventType: eventType,
		Payload:   payload,
	}
	if err := s.outboxWriter.Write(ctx, entry); err != nil {
		return fmt.Errorf("rbac-assign: write outbox entry: %w", err)
	}
	return nil
}

// persistChange wraps a role mutation + outbox write in a single transaction (durable mode)
// or delegates to the legacy dual-write (demo mode).
func (s *Service) persistChange(
	ctx context.Context,
	writeFn func(ctx context.Context) error,
	evt RoleChangedEvent,
	topic string,
) error {
	if s.isDurableMode() {
		// Durable: atomically persist role change + outbox entry.
		// Session revocation is handled asynchronously by the consumer.
		return s.runInTx(ctx, func(txCtx context.Context) error {
			if err := writeFn(txCtx); err != nil {
				return err
			}
			return s.writeOutboxEntry(txCtx, topic, evt)
		})
	}

	// Demo mode: synchronous dual-write (legacy behaviour).
	if err := writeFn(ctx); err != nil {
		return err
	}
	if err := s.sessionRepo.RevokeByUserID(ctx, evt.UserID); err != nil {
		s.logger.Error("rbac-assign: partial commit — role change persisted but session revoke failed; client JWTs retain stale roles until re-login",
			slog.String("user_id", evt.UserID),
			slog.String("role_id", evt.RoleID),
			slog.String("action", evt.Action),
			slog.Any("error", err))
		return fmt.Errorf("rbac-assign: %s succeeded but session revoke failed: %w", evt.Action, err)
	}
	return nil
}

// Assign assigns a role to a user. Idempotent: re-assignment is a no-op.
func (s *Service) Assign(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	evt := RoleChangedEvent{UserID: userID, RoleID: roleID, Action: ActionAssigned}
	writeFn := func(txCtx context.Context) error {
		if err := s.roleRepo.AssignToUser(txCtx, userID, roleID); err != nil {
			return fmt.Errorf("rbac-assign: assign: %w", err)
		}
		return nil
	}

	if err := s.persistChange(ctx, writeFn, evt, TopicRoleAssigned); err != nil {
		return err
	}

	mode := "demo"
	if s.isDurableMode() {
		mode = "durable"
	}
	s.logger.Info("role assigned",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.String("mode", mode))
	return nil
}

// Revoke removes a role from a user. Idempotent: revoking a non-assigned role is a no-op.
// Last-admin guard is enforced atomically by RemoveFromUserIfNotLast (no TOCTOU gap).
func (s *Service) Revoke(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	evt := RoleChangedEvent{UserID: userID, RoleID: roleID, Action: ActionRevoked}
	writeFn := func(txCtx context.Context) error {
		// Atomic count-check + removal eliminates TOCTOU race for last-admin guard.
		if err := s.roleRepo.RemoveFromUserIfNotLast(txCtx, userID, roleID); err != nil {
			return fmt.Errorf("rbac-assign: revoke: %w", err)
		}
		return nil
	}

	if err := s.persistChange(ctx, writeFn, evt, TopicRoleRevoked); err != nil {
		return err
	}

	mode := "demo"
	if s.isDurableMode() {
		mode = "durable"
	}
	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.String("mode", mode))
	return nil
}
