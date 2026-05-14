package rbacassign

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
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
// Revoke routes credential invalidation through the credentialinvalidate funnel,
// which atomically bumps the user's authz_epoch, revokes all active sessions, and
// revokes all refresh chains in the same transaction.
//
// Assign does NOT call the funnel (HIGH-3 decision): granting a role is additive
// and does not represent a credential-security event. The user's existing tokens
// remain valid and reflect the new role after the next re-login.
//
// The role repository reports whether the call actually changed state. On a
// no-op (repeat assign or revoke-non-member), no event is emitted and the
// funnel is not called, preventing false role-change facts.
//
// ref: Watermill SQL outbox + sessionlogin/service.go persistSession pattern.
type Service struct {
	roleRepo    ports.RoleRepository
	invalidator *credentialinvalidate.Invalidator
	txRunner    persistence.CellTxManager
	emitter     outbox.Emitter
	logger      *slog.Logger
}

// Option configures a rbac-assign Service.
type Option func(*Service)

// WithEmitter sets the event emitter used for role-change outbox entries.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the CellTxManager for L2 atomicity. Callers obtain the
// sealed marker via persistence.WrapForCell from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// NewService creates a new rbac-assign service.
// The invalidator is required; it handles credential revocation for Revoke operations.
func NewService(
	roleRepo ports.RoleRepository,
	invalidator *credentialinvalidate.Invalidator,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(roleRepo) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "rbacassign: roleRepo is required")
	}
	if validation.IsNilInterface(invalidator) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "rbacassign: invalidator is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		roleRepo:    roleRepo,
		invalidator: invalidator,
		emitter:     outbox.NewNoopEmitter(),
		logger:      logger,
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
// When callFunnel is true, the credentialinvalidate funnel is called inside the
// same transaction to atomically revoke all credentials for the subject.
// writeFn returns whether the repository actually mutated state, so no-op calls
// never emit false role-change facts or trigger spurious credential revocations.
func (s *Service) persistChange(
	ctx context.Context,
	writeFn func(ctx context.Context) (changed bool, err error),
	evt dto.RoleChangedEvent,
	topic string,
	callFunnel bool,
) (changed bool, err error) {
	err = s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		changed, innerErr = writeFn(txCtx)
		if innerErr != nil {
			return innerErr
		}
		if !changed {
			return nil
		}
		if callFunnel {
			if err := s.invalidator.Apply(txCtx, evt.UserID, session.CredentialEventRoleRevoke); err != nil {
				return fmt.Errorf("rbac-assign: invalidate credentials: %w", err)
			}
		}
		return s.writeOutboxEntry(txCtx, topic, evt)
	})
	return changed, err
}

// Assign assigns a role to a user. Idempotent: re-assignment is a no-op —
// no outbox entry is written and no credential revocation is triggered.
//
// HIGH-3 decision: granting a role is additive and not a credential-security
// event. The funnel is intentionally NOT called on Assign.
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

	changed, err := s.persistChange(ctx, writeFn, evt, dto.TopicRoleAssigned, false)
	if err != nil {
		return err
	}

	s.logger.Info("role assigned",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.Bool("changed", changed))
	return nil
}

// Revoke removes a role from a user. Idempotent: revoking a non-assigned role
// is a no-op — no outbox entry is written and no credential revocation is triggered.
// Last-admin guard is enforced atomically by RemoveFromUserIfNotLast (no TOCTOU gap).
//
// When a state change occurs, the credentialinvalidate funnel runs inside the same
// transaction, atomically bumping the authz_epoch and revoking all active sessions
// and refresh chains.
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

	changed, err := s.persistChange(ctx, writeFn, evt, dto.TopicRoleRevoked, true)
	if err != nil {
		return err
	}

	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.Bool("changed", changed))
	return nil
}
