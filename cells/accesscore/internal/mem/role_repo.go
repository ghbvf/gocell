package mem

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

var _ ports.RoleRepository = (*RoleRepository)(nil)

// RoleRepository is the in-memory implementation of ports.RoleRepository.
// It is always vended by Store.RoleRepository() so the shared mutex covers
// any cross-repo invariant — most importantly, CountEffectiveAdmins and the
// admin branch of RemoveFromUserIfNotLast read user.Status atomically with
// the role_assignments state, mirroring the PG advisory-lock + FOR UPDATE
// guarantees in role_repo.go.
//
// All methods are tenant-scoped (#1337 PR-2): the tenant.TenantID positional
// parameter selects the per-tenant partition of the underlying maps.
type RoleRepository struct {
	store *Store
}

// SeedRole adds a role directly into the store for testing purposes. It
// always acquires the lock because seed calls are never inside a RunInTx.
func (r *RoleRepository) SeedRole(t tenant.TenantID, role *domain.Role) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	tRoles := r.store.tenantRoles(string(t))
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	tRoles[role.ID] = &clone
}

// Create persists a new role within the tenant. Idempotent: if a role with
// the same ID already exists in the tenant, it is silently overwritten
// (upsert semantics for seed/bootstrap). Safe to call both inside and outside
// a RunInTx closure; see the lock contract on UserRepository.
func (r *RoleRepository) Create(ctx context.Context, t tenant.TenantID, role *domain.Role) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}
	tRoles := r.store.tenantRoles(string(t))
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	tRoles[role.ID] = &clone
	return nil
}

// GetByID returns the Role with the given ID within the tenant. Safe to call
// both inside and outside a RunInTx closure; see the lock contract on
// UserRepository.
func (r *RoleRepository) GetByID(ctx context.Context, t tenant.TenantID, id string) (*domain.Role, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tRoles := r.store.tenantRoles(string(t))
	role, ok := tRoles[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", id))))
	}
	clone := *role
	return &clone, nil
}

// GetByUserID returns all roles assigned to userID within the tenant. Safe to
// call both inside and outside a RunInTx closure; see the lock contract on
// UserRepository.
func (r *RoleRepository) GetByUserID(ctx context.Context, t tenant.TenantID, userID string) ([]*domain.Role, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tUserRoles := r.store.tenantUserRoles(string(t))
	roleIDs, ok := tUserRoles[userID]
	if !ok {
		return []*domain.Role{}, nil
	}

	tRoles := r.store.tenantRoles(string(t))
	var result []*domain.Role
	for rid := range roleIDs {
		if role, ok := tRoles[rid]; ok {
			clone := *role
			result = append(result, &clone)
		}
	}
	return result, nil
}

// AssignToUser assigns roleID to userID within the tenant. Safe to call both
// inside and outside a RunInTx closure; see the lock contract on
// UserRepository.
func (r *RoleRepository) AssignToUser(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tRoles := r.store.tenantRoles(string(t))
	if _, ok := tRoles[roleID]; !ok {
		return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("role_id=%s", roleID))))
	}

	tUserRoles := r.store.tenantUserRoles(string(t))
	if tUserRoles[userID] == nil {
		tUserRoles[userID] = make(map[string]struct{})
	}
	if _, already := tUserRoles[userID][roleID]; already {
		return false, nil
	}
	tUserRoles[userID][roleID] = struct{}{}
	return true, nil
}

// RemoveFromUser removes roleID from userID within the tenant unconditionally.
// Safe to call both inside and outside a RunInTx closure.
func (r *RoleRepository) RemoveFromUser(ctx context.Context, t tenant.TenantID, userID, roleID string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tUserRoles := r.store.tenantUserRoles(string(t))
	if roles, ok := tUserRoles[userID]; ok {
		delete(roles, roleID)
	}
	return nil
}

// RemoveFromUserIfNotLast atomically removes the admin role from a user only
// when removing the assignment would not leave the tenant with zero effective
// admins. Mirrors the PG removeIfNotLastSQL per-tenant CTE semantics (#1337 PR-2).
func (r *RoleRepository) RemoveFromUserIfNotLast(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tUserRoles := r.store.tenantUserRoles(string(t))
	// Check if user actually holds the role.
	userHoldsRole := false
	if roles, ok := tUserRoles[userID]; ok {
		_, userHoldsRole = roles[roleID]
	}
	if !userHoldsRole {
		// Revoking a role the user does not hold is an idempotent no-op.
		return false, nil
	}

	if roleID == auth.RoleAdmin {
		// Short-circuit 1: target non-active → removal cannot reduce the
		// effective-admin count. Aligns with migration-046 trigger.
		targetIsActive := false
		if u, ok := r.store.usersByID[userID]; ok {
			targetIsActive = u.Status() == domain.StatusActive
		}
		if targetIsActive {
			// Short-circuit 2: target IS effective admin — require at least
			// one OTHER effective admin to remain in this tenant.
			if r.countOtherEffectiveAdminsLocked(string(t), userID) == 0 {
				return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
					"cannot revoke admin: removing this assignment would leave the system with no effective admin; assign admin to an active user first",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("role_id=%q user_id=%q", roleID, userID))))
			}
		}
	}

	delete(tUserRoles[userID], roleID)
	return true, nil
}

// ListByUserID returns paginated roles for userID within the tenant sorted per
// params. Safe to call both inside and outside a RunInTx closure.
func (r *RoleRepository) ListByUserID(
	ctx context.Context, t tenant.TenantID, userID string, params query.ListParams,
) ([]*domain.Role, error) {
	roles, err := r.rolesByUserSnapshot(ctx, t, userID)
	if err != nil {
		return nil, err
	}

	query.Sort(roles, params.Sort, compareRoleField)
	result, err := query.ApplyCursor(roles, params, roleFieldValue)
	if err != nil {
		return nil, fmt.Errorf("role-repo: list-by-user: %w", err)
	}
	return result, nil
}

func (r *RoleRepository) rolesByUserSnapshot(ctx context.Context, t tenant.TenantID, userID string) ([]*domain.Role, error) {
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}
	tUserRoles := r.store.tenantUserRoles(string(t))
	roleIDs, ok := tUserRoles[userID]
	if !ok {
		return []*domain.Role{}, nil
	}

	tRoles := r.store.tenantRoles(string(t))
	roles := make([]*domain.Role, 0, len(roleIDs))
	for rid := range roleIDs {
		if role, ok := tRoles[rid]; ok {
			clone := *role
			clone.Permissions = make([]domain.Permission, len(role.Permissions))
			copy(clone.Permissions, role.Permissions)
			roles = append(roles, &clone)
		}
	}
	return roles, nil
}

func compareRoleField(a, b *domain.Role, field string) int {
	switch field {
	case "name":
		return cmp.Compare(a.Name, b.Name)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

func roleFieldValue(r *domain.Role, field string) any {
	switch field {
	case "name":
		return r.Name
	case "id":
		return r.ID
	default:
		return ""
	}
}

// CountByRole returns the total count of role_assignments for roleID within
// the tenant, regardless of user status. Used for bootstrap idempotency
// (adminprovision); MUST NOT be used as the last-admin invariant counter —
// see CountEffectiveAdmins.
func (r *RoleRepository) CountByRole(ctx context.Context, t tenant.TenantID, roleID string) (int, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}
	tUserRoles := r.store.tenantUserRoles(string(t))
	count := 0
	for _, roleIDs := range tUserRoles {
		if _, ok := roleIDs[roleID]; ok {
			count++
		}
	}
	return count, nil
}

// CountEffectiveAdmins returns the number of users that are simultaneously
// status='active' AND hold the admin role within the tenant. Satisfies the
// domain.EffectiveAdminCounter sealed interface (S4.0 invariant counter).
func (r *RoleRepository) CountEffectiveAdmins(ctx context.Context, t tenant.TenantID) (int, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}
	tUserRoles := r.store.tenantUserRoles(string(t))
	count := 0
	for userID, roleIDs := range tUserRoles {
		if _, hasAdmin := roleIDs[auth.RoleAdmin]; !hasAdmin {
			continue
		}
		u, ok := r.store.usersByID[userID]
		if !ok {
			continue
		}
		if u.Status() == domain.StatusActive {
			count++
		}
	}
	return count, nil
}

// EffectiveAdminExists implements ports.RoleRepository — see the port godoc
// for fast-path semantics. Returns true on the first match within the tenant.
func (r *RoleRepository) EffectiveAdminExists(ctx context.Context, t tenant.TenantID) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}
	tUserRoles := r.store.tenantUserRoles(string(t))
	for userID, roleIDs := range tUserRoles {
		if _, hasAdmin := roleIDs[auth.RoleAdmin]; !hasAdmin {
			continue
		}
		u, ok := r.store.usersByID[userID]
		if !ok {
			continue
		}
		if u.Status() == domain.StatusActive {
			return true, nil
		}
	}
	return false, nil
}

// countOtherEffectiveAdminsLocked is the internal helper used by
// RemoveFromUserIfNotLast. It counts effective admins (status='active' AND
// admin role) EXCLUDING excludeUserID, within tenantID. Caller MUST already
// hold store.mu (write or read).
func (r *RoleRepository) countOtherEffectiveAdminsLocked(tenantID, excludeUserID string) int {
	tUserRoles := r.store.tenantUserRoles(tenantID)
	count := 0
	for userID, roleIDs := range tUserRoles {
		if userID == excludeUserID {
			continue
		}
		if _, hasAdmin := roleIDs[auth.RoleAdmin]; !hasAdmin {
			continue
		}
		u, ok := r.store.usersByID[userID]
		if !ok {
			continue
		}
		if u.Status() == domain.StatusActive {
			count++
		}
	}
	return count
}
