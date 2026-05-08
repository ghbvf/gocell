package rbacassign

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialrevoke"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// Service handles RBAC role assignment and revocation.
//
// Role mutation, credential revocation, and optional event.role.{assigned,revoked}.v1
// emission run in one transaction. The sessionlogout consumer remains an
// idempotent replay/compensation path for role-change events, not the primary
// invalidation protocol.
//
// The role repository reports whether the call actually changed state. On a
// no-op (repeat assign or revoke-non-member), no event is emitted and no
// session revoke is triggered, preventing false role-change facts.
//
// ref: Watermill SQL outbox + sessionlogin/service.go persistSession pattern.
type Service struct {
	userRepo      ports.UserRepository
	roleRepo      ports.RoleRepository
	sessionRepo   ports.SessionRepository
	refreshStore  refresh.Store
	txRunner      persistence.TxRunner
	emitter       outbox.Emitter
	durableEvents bool
	logger        *slog.Logger
}

// Option configures a rbac-assign Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
			s.durableEvents = true
		}
	}
}

// WithTxManager sets the TxRunner for L2 atomicity (must be paired with WithEmitter).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// NewService creates a new rbac-assign service. Role mutation, credential
// revocation, and optional event emission run inside the configured TxRunner.
// The role-change consumer is retained as idempotent compensation/replay.
func NewService(
	userRepo ports.UserRepository,
	roleRepo ports.RoleRepository,
	sessionRepo ports.SessionRepository,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(userRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "rbacassign.NewService: userRepo must not be nil")
	}
	if validation.IsNilInterface(roleRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "rbacassign.NewService: roleRepo must not be nil")
	}
	if validation.IsNilInterface(sessionRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "rbacassign.NewService: sessionRepo must not be nil")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "rbacassign.NewService: refreshStore must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		userRepo:     userRepo,
		roleRepo:     roleRepo,
		sessionRepo:  sessionRepo,
		refreshStore: refreshStore,
		emitter:      outbox.NewNoopEmitter(),
		logger:       logger,
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
		if _, innerErr = s.userRepo.GetByIDForUpdate(txCtx, evt.UserID); innerErr != nil {
			return fmt.Errorf("rbac-assign: lock user: %w", innerErr)
		}
		changed, innerErr = writeFn(txCtx)
		if innerErr != nil {
			return innerErr
		}
		if !changed {
			return nil
		}
		if err := credentialrevoke.User(txCtx, s.sessionRepo, s.refreshStore, evt.UserID, "rbac-assign: "+evt.Action); err != nil {
			return err
		}
		if !s.durableEvents {
			return nil
		}
		return s.writeOutboxEntry(txCtx, topic, evt)
	})
	return changed, err
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
	if s.durableEvents {
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
	if s.durableEvents {
		mode = "durable"
	}
	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.String("mode", mode),
		slog.Bool("changed", changed))
	return nil
}
