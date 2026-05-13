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
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Service handles RBAC role assignment and revocation.
//
// When the Cell injects an emitter for asynchronous role-change delivery, the
// role mutation and event.role.{assigned,revoked}.v1 emission run in one
// transaction and session revocation moves to the sessionlogout consumer.
//
// With the default noop emitter, the service preserves the in-process
// role-change + session-revoke flow used by demo and in-memory wiring.
//
// The role repository reports whether the call actually changed state. On a
// no-op (repeat assign or revoke-non-member), no event is emitted and no
// session revoke is triggered, preventing false role-change facts.
//
// ref: Watermill SQL outbox + sessionlogin/service.go persistSession pattern.
type Service struct {
	roleRepo              ports.RoleRepository
	sessionStore          session.Store
	txRunner              persistence.CellTxManager
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

// WithTxManager sets the CellTxManager for L2 atomicity (must be paired with
// WithEmitter). Callers obtain the sealed marker via persistence.WrapForCell
// from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// NewService creates a new rbac-assign service.
// When WithEmitter is provided, session revocation is delegated to the
// sessionlogout consumer. With default options, the legacy in-process revoke
// path is used.
func NewService(
	roleRepo ports.RoleRepository,
	sessionStore session.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	s := &Service{
		roleRepo:              roleRepo,
		sessionStore:          sessionStore,
		emitter:               outbox.NewNoopEmitter(),
		syncSessionRevocation: true,
		logger:                logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "rbacassign: TxRunner required; use WithTxManager")
	}
	return s, nil
}

// writeOutboxEntry writes a role-change outbox entry. Called inside a transaction.
func (s *Service) writeOutboxEntry(ctx context.Context, eventType string, evt dto.RoleChangedEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rbac-assign: marshal role-changed event: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.MustNewEntryID(),
		EventType: eventType,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("rbac-assign: emit role-changed event: %w", err)
	}
	return nil
}

// persistChange wraps a role mutation in the configured transaction runner.
// When an async emitter is configured it also emits the role-change event; with
// the default noop emitter it keeps the in-process session revoke path. writeFn
// returns whether the repository actually mutated state, so no-op calls never
// emit false role-change facts.
func (s *Service) persistChange(
	ctx context.Context,
	writeFn func(ctx context.Context) (changed bool, err error),
	evt dto.RoleChangedEvent,
	topic string,
) (changed bool, err error) {
	err = s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
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
	if err := s.sessionStore.RevokeForSubject(ctx, evt.UserID, session.CredentialEventRoleRevoke); err != nil {
		s.logger.Error("rbac-assign: partial commit — role change persisted but session revoke failed;"+
			" client JWTs retain stale roles until re-login",
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
//
// Note: mirror-image of Revoke is intentional — independent public API + repo
// method + topic; extracting a helper would parameterize 4 call sites and
// obscure the per-action audit trail.
//

func (s *Service) Assign(ctx context.Context, userID, roleID string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthRBACInvalidInput,
		validation.F("userId", userID),
		validation.F("roleId", roleID),
	); err != nil {
		return err
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
//

func (s *Service) Revoke(ctx context.Context, userID, roleID string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthRBACInvalidInput,
		validation.F("userId", userID),
		validation.F("roleId", roleID),
	); err != nil {
		return err
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
