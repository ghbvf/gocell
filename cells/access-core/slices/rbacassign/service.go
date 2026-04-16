package rbacassign

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Service handles RBAC role assignment and revocation (L0 — pure repo operations).
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
	// Fail-closed: if session revocation fails, the role change is not reported
	// as successful to prevent stale-role window.
	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
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
	// Fail-closed: if session revocation fails, the role change is not reported
	// as successful to prevent stale-role window.
	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("rbac-assign: revoke succeeded but session revoke failed: %w", err)
	}

	s.logger.Info("role revoked",
		slog.String("user_id", userID),
		slog.String("role_id", roleID))
	return nil
}
