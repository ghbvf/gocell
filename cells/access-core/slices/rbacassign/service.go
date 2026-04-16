package rbacassign

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Service handles RBAC role assignment and revocation (L0 — pure repo operations).
//
// Consistency semantic (at-least-once with partial-commit window):
//
// Assign/Revoke perform two sequential writes to independent repositories:
//  1. roleRepo.{AssignToUser,RemoveFromUserIfNotLast} — authoritative role state
//  2. sessionRepo.RevokeByUserID — forces JWT re-issue to pick up new roles
//
// If step 2 fails, step 1 is already committed. The operation returns an error
// to the caller (fail-closed), but the role change is persisted. Callers MUST
// treat both operations as idempotent and retry on error.
//
// Partial-commit window (step 1 succeeded, step 2 failed):
//   - Stale JWT: existing tokens retain old roles until natural expiry or re-login
//   - Log: Error level emitted at line-60/line-80 with user_id + role_id for observability
//   - Recovery: caller retry will re-invoke both steps; step 1 is no-op, step 2 retries
//
// TODO(H1-7): Migrate to transactional outbox — write role change + `role.*.v1`
// event atomically, consume event asynchronously to revoke sessions. This would
// provide at-least-once guarantees across both sides (ref: Watermill outbox pattern).
type Service struct {
	roleRepo    ports.RoleRepository
	sessionRepo ports.SessionRepository
	logger      *slog.Logger
}

// NewService creates a new rbac-assign service.
// sessionRepo is used to revoke active sessions on role change so that
// the JWT (which embeds roles) is forced to refresh.
func NewService(roleRepo ports.RoleRepository, sessionRepo ports.SessionRepository, logger *slog.Logger) *Service {
	return &Service{roleRepo: roleRepo, sessionRepo: sessionRepo, logger: logger}
}

// Assign assigns a role to a user. Idempotent: re-assignment is a no-op.
func (s *Service) Assign(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	if err := s.roleRepo.AssignToUser(ctx, userID, roleID); err != nil {
		return fmt.Errorf("rbac-assign: assign: %w", err)
	}

	// Revoke active sessions so user must re-login to get updated JWT roles.
	// Fail-closed: role change persisted, but we return error so caller retries.
	// Log at Error level to make the partial-commit window observable.
	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		s.logger.Error("rbac-assign: partial commit — role assigned but session revoke failed; client JWTs retain stale roles until re-login",
			slog.String("user_id", userID),
			slog.String("role_id", roleID),
			slog.String("error", err.Error()))
		return fmt.Errorf("rbac-assign: assign succeeded but session revoke failed: %w", err)
	}

	s.logger.Info("role assigned",
		slog.String("user_id", userID),
		slog.String("role_id", roleID))
	return nil
}

// Revoke removes a role from a user. Idempotent: revoking a non-assigned role is a no-op.
// Active sessions are revoked to force re-login with updated JWT roles.
// Last-admin guard is enforced atomically by RemoveFromUserIfNotLast (no TOCTOU gap).
func (s *Service) Revoke(ctx context.Context, userID, roleID string) error {
	if userID == "" || roleID == "" {
		return errcode.New(errcode.ErrAuthRBACInvalidInput, "userId and roleId are required")
	}

	// Atomic count-check + removal eliminates TOCTOU race for last-admin guard.
	if err := s.roleRepo.RemoveFromUserIfNotLast(ctx, userID, roleID); err != nil {
		return fmt.Errorf("rbac-assign: revoke: %w", err)
	}

	// Revoke active sessions so user must re-login to get updated JWT roles.
	// Fail-closed: role change persisted, but we return error so caller retries.
	// Log at Error level to make the partial-commit window observable.
	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		s.logger.Error("rbac-assign: partial commit — role revoked but session revoke failed; client JWTs retain stale roles until re-login",
			slog.String("user_id", userID),
			slog.String("role_id", roleID),
			slog.String("error", err.Error()))
		return fmt.Errorf("rbac-assign: revoke succeeded but session revoke failed: %w", err)
	}

	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID))
	return nil
}
