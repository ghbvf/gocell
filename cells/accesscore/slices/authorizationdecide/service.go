// Package authorizationdecide implements the authorization-decide slice:
// RBAC-based authorization decisions. Implements runtime/auth.Authorizer.
package authorizationdecide

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/scopedtx"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time check: Service implements auth.Authorizer.
var _ auth.Authorizer = (*Service)(nil)

// Service implements RBAC authorization decisions.
type Service struct {
	roleRepo ports.RoleRepository       `gocell:"required" gocellErr:"authorizationdecide: roleRepo is required"`                                                                                                    //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	txRunner persistence.CellTxManager `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"authorizationdecide: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
}

// Option configures Service.
type Option func(*Service)

// WithTxManager injects a CellTxManager. Typed-nil inputs are ignored; the
// subsequent factory call will fail with ErrValidationFailed.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// NewService creates an authorization-decide Service. Returns an error when
// roleRepo is nil (typed-nil or bare). logger defaults to slog.Default() when
// nil to keep the no-args convenience callers expect.
func NewService(roleRepo ports.RoleRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{roleRepo: roleRepo, logger: logger}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Authorize checks whether the subject has a role granting the action on the
// resource within the caller's tenant.
func (s *Service) Authorize(ctx context.Context, subject, resource, action string) (bool, error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("authorization-decide: tenant: %w", err)
	}
	roles, err := scopedtx.Do(ctx, s.txRunner, tid, func(txCtx context.Context) ([]*domain.Role, error) {
		return s.roleRepo.GetByUserID(txCtx, tid, subject)
	})
	if err != nil {
		return false, fmt.Errorf("authorization-decide: get roles: %w", err)
	}

	for _, role := range roles {
		if role.HasPermission(resource, action) {
			s.logger.Debug(
				"authorization granted",
				slog.String("subject", subject),
				slog.String("resource", resource),
				slog.String("action", action),
				slog.String("role", role.Name),
			)
			return true, nil
		}
	}

	return false, nil
}
