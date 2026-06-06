// Package rbaccheck implements the rbac-check slice: HasRole / ListRoles
// queries for a given user.
package rbaccheck

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/scopedtx"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
)

var roleSort = []query.SortColumn{
	{Name: "name", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements RBAC query operations.
type Service struct {
	roleRepo ports.RoleRepository      `gocell:"required" gocellErr:"rbac-check: roleRepo is required"`                                                                           //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	codec    *query.CursorCodec        `gocell:"required" gocellKind:"KindInternal" gocellCode:"ErrCellMissingCodec" gocellErr:"rbac-check: cursor codec is required"`            //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	txRunner persistence.CellTxManager `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"rbac-check: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
	runMode  query.RunMode
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

// NewService creates an rbac-check Service. roleRepo and codec are required;
// logger defaults to slog.Default() when nil.
func NewService(
	roleRepo ports.RoleRepository,
	codec *query.CursorCodec,
	logger *slog.Logger,
	runMode query.RunMode,
	opts ...Option,
) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{roleRepo: roleRepo, codec: codec, logger: logger, runMode: runMode}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// HasRole checks if a user has the specified role within the caller's tenant.
func (s *Service) HasRole(ctx context.Context, userID, roleName string) (bool, error) {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthRBACInvalidInput,
		validation.F("userID", userID),
		validation.F("roleName", roleName),
	); err != nil {
		return false, err
	}

	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("rbac-check: has-role: tenant: %w", err)
	}

	roles, err := scopedtx.Do(ctx, s.txRunner, tid, func(txCtx context.Context) ([]*domain.Role, error) {
		return s.roleRepo.GetByUserID(txCtx, tid, userID)
	})
	if err != nil {
		return false, fmt.Errorf("rbac-check: has role: %w", err)
	}

	for _, r := range roles {
		if r.Name == roleName {
			return true, nil
		}
	}
	return false, nil
}

// ListRoles returns a paginated page of roles assigned to userID.
func (s *Service) ListRoles(ctx context.Context, userID string, pageReq query.PageParams) (query.PageResult[*domain.Role], error) {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthRBACInvalidInput,
		validation.F("userID", userID),
	); err != nil {
		return query.PageResult[*domain.Role]{}, err
	}

	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return query.PageResult[*domain.Role]{}, fmt.Errorf("rbac-check: list-roles: tenant: %w", err)
	}
	txRunner := s.txRunner
	qctx := query.QueryContext("endpoint", "rbac-list-roles", "userId", userID)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Role]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       roleSort,
		QueryCtx:   qctx,
		Fetch: func(fetchCtx context.Context, params query.ListParams) ([]*domain.Role, error) {
			roles, err := scopedtx.Do(fetchCtx, txRunner, tid, func(txCtx context.Context) ([]*domain.Role, error) {
				return s.roleRepo.ListByUserID(txCtx, tid, userID, params)
			})
			if err != nil {
				return nil, fmt.Errorf("rbac-check: list roles: %w", err)
			}
			return roles, nil
		},
		Extract: func(r *domain.Role) []any {
			return []any{r.Name, r.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "rbac-check"),
		RunMode:     s.runMode,
	})
}
