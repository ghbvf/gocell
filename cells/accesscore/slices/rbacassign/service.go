package rbacassign

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// actorFromContext returns the caller identity to record in
// RoleChangedEvent.ActorID. rbacassign is an internal-listener-only slice
// reached exclusively through service-token authentication; the originating
// cell is therefore the authoritative actor for audit purposes (auditcore's
// role-event consumer uses ActorRequireExplicit mode and DLX-rejects
// payloads with an empty ActorID). Returns "" when no principal is present
// (unit-test path that constructs the service with context.Background) —
// callers in that mode opt out of audit ledger evidence by construction.
func actorFromContext(ctx context.Context) string {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return ""
	}
	if p.CallerCellID != "" {
		return p.CallerCellID
	}
	return p.Subject
}

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
	roleRepo    ports.RoleRepository              `gocell:"required" gocellErr:"rbacassign: roleRepo is required"`                 //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	userRepo    ports.UserRepository              `gocell:"required" gocellErr:"rbacassign: userRepo is required"`                 //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	invalidator *credentialinvalidate.Invalidator `gocell:"required" gocellErr:"rbacassign: invalidator is required"`              //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	txRunner    persistence.CellTxManager         `gocell:"required" gocellErr:"rbacassign: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clk         clock.Clock                       `gocell:"required" gocellErr:"rbacassign.NewService: clock.Clock required"`      //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter     outbox.CellEmitter
	logger      *slog.Logger
}

// Option configures a rbac-assign Service.
type Option func(*Service)

// WithEmitter sets the event emitter used for role-change outbox entries.
// Accepts a sealed outbox.CellEmitter; typed-nil inputs are silently ignored
// (builder-option semantics, see runtime-api.md Option Pattern Layering);
// the service defaults to outbox.DemoCellEmitter() (demo mode wrapper).
func WithEmitter(e outbox.CellEmitter) Option {
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
	clk clock.Clock,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	invalidator *credentialinvalidate.Invalidator,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "rbacassign.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		roleRepo:    roleRepo,
		userRepo:    userRepo,
		invalidator: invalidator,
		clk:         clk,
		emitter:     outbox.DemoCellEmitter(),
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// persistChange wraps a role mutation in the configured transaction runner.
// When callFunnel is true, the credentialinvalidate funnel is called inside the
// same transaction to atomically revoke all credentials for the subject.
// writeFn returns whether the repository actually mutated state, so no-op calls
// never emit false role-change facts or trigger spurious credential revocations.
//
// emitFn carries the per-caller emit logic with a const topic; pushing the
// topic out of this function lets EMIT-DECL-COVER-01's literal-site scan see
// each caller's const, rather than an opaque `topic string` parameter.
//
// tid scopes the credential invalidation to the correct tenant. Callers derive
// it from the target user (GetByID by-PK carve-out) before calling persistChange.
func (s *Service) persistChange(
	ctx context.Context,
	tid tenant.TenantID,
	writeFn func(ctx context.Context) (changed bool, err error),
	evt dto.RoleChangedEvent,
	emitFn func(ctx context.Context, evt dto.RoleChangedEvent) error,
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
			if err := s.invalidator.Apply(txCtx, tid, evt.UserID, session.CredentialEventRoleRevoke); err != nil {
				return fmt.Errorf("rbac-assign: invalidate credentials: %w", err)
			}
		}
		return emitFn(txCtx, evt)
	})
	return changed, err
}

// Assign assigns a role to a user. Idempotent: re-assignment is a no-op —
// no outbox entry is written and no credential revocation is triggered.
//
// HIGH-3 decision: granting a role is additive and not a credential-security
// event. The funnel is intentionally NOT called on Assign.
func (s *Service) Assign(ctx context.Context, userID, roleID string) error {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthRBACInvalidInput,
		validation.F("userId", userID),
		validation.F("roleId", roleID),
	); err != nil {
		return err
	}

	// Option B (#1337 PR-2a): this InternalListener / service-token endpoint has
	// a tenant-less caller, so the assignment tenant is derived from the TARGET
	// user via the by-global-PK tenant-deriving GetByID carve-out — "assign role
	// to user U" inherently scopes to U's tenant. A missing user surfaces as the
	// GetByID not-found error.
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("rbac-assign: assign: resolve user tenant: %w", err)
	}
	tid := u.TenantID
	evt := dto.RoleChangedEvent{UserID: userID, RoleID: roleID, Action: dto.ActionAssigned, ActorID: actorFromContext(ctx)}
	writeFn := func(txCtx context.Context) (bool, error) {
		changed, err := s.roleRepo.AssignToUser(txCtx, tid, userID, roleID)
		if err != nil {
			return false, fmt.Errorf("rbac-assign: assign: %w", err)
		}
		return changed, nil
	}

	emitFn := func(txCtx context.Context, evt dto.RoleChangedEvent) error {
		return outbox.Emit(txCtx, s.clk, s.emitter, dto.TopicRoleAssigned, evt)
	}
	changed, err := s.persistChange(ctx, tid, writeFn, evt, emitFn, false)
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthRBACInvalidInput,
		validation.F("userId", userID),
		validation.F("roleId", roleID),
	); err != nil {
		return err
	}

	// Option B (#1337 PR-2a): tenant derived from the target user (see Assign).
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("rbac-assign: revoke: resolve user tenant: %w", err)
	}
	tid := u.TenantID
	evt := dto.RoleChangedEvent{UserID: userID, RoleID: roleID, Action: dto.ActionRevoked, ActorID: actorFromContext(ctx)}
	writeFn := func(txCtx context.Context) (bool, error) {
		// Atomic count-check + removal eliminates TOCTOU race for last-admin guard.
		changed, err := s.roleRepo.RemoveFromUserIfNotLast(txCtx, tid, userID, roleID)
		if err != nil {
			return false, fmt.Errorf("rbac-assign: revoke: %w", err)
		}
		return changed, nil
	}

	emitFn := func(txCtx context.Context, evt dto.RoleChangedEvent) error {
		return outbox.Emit(txCtx, s.clk, s.emitter, dto.TopicRoleRevoked, evt)
	}
	changed, err := s.persistChange(ctx, tid, writeFn, evt, emitFn, true)
	if err != nil {
		return err
	}

	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID),
		slog.Bool("changed", changed))
	return nil
}
