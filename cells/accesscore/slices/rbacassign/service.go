package rbacassign

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
// Idempotent no-op handling (both modes): the role repository reports whether
// the call actually changed state. On no-op (repeat assign or revoke-non-member),
// no outbox entry is written and no session revoke is triggered — the operation
// is externally invisible, preventing false role-change facts.
//
// ref: Watermill SQL outbox + sessionlogin/service.go persistSession pattern.
type Service struct {
	roleRepo              ports.RoleRepository
	sessionRepo           ports.SessionRepository
	txRunner              persistence.TxRunner
	emitter               outbox.Emitter
	syncSessionRevocation bool
	logger                *slog.Logger
}

// Option configures a rbac-assign Service.
type Option func(*Service)

// WithEmitter sets the event emitter and disables in-process session revoke.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
			s.syncSessionRevocation = false
		}
	}
}

// WithOutboxWriter adapts an outbox.Writer for existing tests and wiring.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) {
		if e, err := outbox.NewWriterEmitter(w); err == nil {
			s.emitter = e
			s.syncSessionRevocation = false
		}
	}
}

// WithTxManager sets the TxRunner for L2 atomicity (must be paired with WithOutboxWriter).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
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
		roleRepo:              roleRepo,
		sessionRepo:           sessionRepo,
		txRunner:              persistence.NoopTxRunner{},
		emitter:               outbox.NewNoopEmitter(),
		syncSessionRevocation: true,
		logger:                logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// runInTx wraps fn in a transaction runner.
func (s *Service) runInTx(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// writeOutboxEntry writes a role-change outbox entry. Called inside a transaction.
func (s *Service) writeOutboxEntry(ctx context.Context, eventType string, evt dto.RoleChangedEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rbac-assign: marshal role-changed event: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: eventType,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("rbac-assign: emit role-changed event: %w", err)
	}
	return nil
}

// persistChange wraps a role mutation + outbox write in a single transaction (durable mode)
// or delegates to the legacy dual-write (demo mode). writeFn returns whether the repository
// actually mutated state; the outbox entry (durable) or session revoke (demo) is skipped
// on a no-op so repeat calls do not publish false role-change facts.
func (s *Service) persistChange(
	ctx context.Context,
	writeFn func(ctx context.Context) (changed bool, err error),
	evt dto.RoleChangedEvent,
	topic string,
) (changed bool, err error) {
	err = s.runInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		changed, innerErr = writeFn(txCtx)
		if innerErr != nil {
			return innerErr
		}
		if !changed || s.syncSessionRevocation {
			return nil
		}
		return s.writeOutboxEntry(txCtx, topic, evt)
	})
	if err != nil || !s.syncSessionRevocation {
		return changed, err
	}
	if !changed {
		return false, nil
	}
	if err := s.sessionRepo.RevokeByUserID(ctx, evt.UserID); err != nil {
		s.logger.Error("rbac-assign: partial commit — role change persisted but session revoke failed; client JWTs retain stale roles until re-login",
			slog.String("user_id", evt.UserID),
			slog.String("role_id", evt.RoleID),
			slog.String("action", evt.Action),
			slog.Any("error", err))
		return true, fmt.Errorf("rbac-assign: %s succeeded but session revoke failed: %w", evt.Action, err)
	}
	return true, nil
}

// Assign assigns a role to a user. Idempotent: re-assignment is a no-op —
// no outbox entry is written and no session is revoked.
func (s *Service) Assign(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	evt := dto.RoleChangedEvent{UserID: userID, RoleID: roleID, Action: dto.ActionAssigned}
	writeFn := func(txCtx context.Context) (bool, error) {
		changed, err := s.roleRepo.AssignToUser(txCtx, userID, roleID)
		if err != nil {
			return false, fmt.Errorf("rbac-assign: assign: %w", err)
		}
		return changed, nil
	}

	changed, err := s.persistChange(ctx, writeFn, evt, dto.TopicRoleAssigned)
	if err != nil {
		return err
	}

	mode := "demo"
	if !s.syncSessionRevocation {
		mode = "durable"
	}
	s.logger.Info("role assigned",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.String("mode", mode),
		slog.Bool("changed", changed))
	return nil
}

// Revoke removes a role from a user. Idempotent: revoking a non-assigned role
// is a no-op — no outbox entry is written and no session is revoked. Last-admin
// guard is enforced atomically by RemoveFromUserIfNotLast (no TOCTOU gap).
func (s *Service) Revoke(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	evt := dto.RoleChangedEvent{UserID: userID, RoleID: roleID, Action: dto.ActionRevoked}
	writeFn := func(txCtx context.Context) (bool, error) {
		// Atomic count-check + removal eliminates TOCTOU race for last-admin guard.
		changed, err := s.roleRepo.RemoveFromUserIfNotLast(txCtx, userID, roleID)
		if err != nil {
			return false, fmt.Errorf("rbac-assign: revoke: %w", err)
		}
		return changed, nil
	}

	changed, err := s.persistChange(ctx, writeFn, evt, dto.TopicRoleRevoked)
	if err != nil {
		return err
	}

	mode := "demo"
	if !s.syncSessionRevocation {
		mode = "durable"
	}
	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.String("mode", mode),
		slog.Bool("changed", changed))
	return nil
}
