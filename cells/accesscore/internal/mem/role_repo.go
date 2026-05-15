package mem

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

var _ ports.RoleRepository = (*RoleRepository)(nil)

// RoleRepository is the in-memory implementation of ports.RoleRepository.
// It is always vended by Store.RoleRepository() so the shared mutex covers
// any cross-repo invariant — most importantly, CountEffectiveAdmins and the
// admin branch of RemoveFromUserIfNotLast read user.Status atomically with
// the role_assignments state, mirroring the PG advisory-lock + FOR UPDATE
// guarantees in role_repo.go.
type RoleRepository struct {
	store *Store
}

// SeedRole adds a role for testing purposes.
func (r *RoleRepository) SeedRole(role *domain.Role) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	r.store.roles[role.ID] = &clone
}

// Create persists a new role. Idempotent: if a role with the same ID already
// exists, it is silently overwritten (upsert semantics for seed/bootstrap).
func (r *RoleRepository) Create(_ context.Context, role *domain.Role) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	r.store.roles[role.ID] = &clone
	return nil
}

func (r *RoleRepository) GetByID(_ context.Context, id string) (*domain.Role, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()

	role, ok := r.store.roles[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	clone := *role
	return &clone, nil
}

func (r *RoleRepository) GetByUserID(_ context.Context, userID string) ([]*domain.Role, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()

	roleIDs, ok := r.store.userRoles[userID]
	if !ok {
		return []*domain.Role{}, nil
	}

	var result []*domain.Role
	for rid := range roleIDs {
		if role, ok := r.store.roles[rid]; ok {
			clone := *role
			result = append(result, &clone)
		}
	}
	return result, nil
}

func (r *RoleRepository) AssignToUser(_ context.Context, userID, roleID string) (bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	if _, ok := r.store.roles[roleID]; !ok {
		return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("role_id=%s", roleID)))
	}

	if r.store.userRoles[userID] == nil {
		r.store.userRoles[userID] = make(map[string]struct{})
	}
	if _, already := r.store.userRoles[userID][roleID]; already {
		return false, nil
	}
	r.store.userRoles[userID][roleID] = struct{}{}
	return true, nil
}

func (r *RoleRepository) RemoveFromUser(_ context.Context, userID, roleID string) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	if roles, ok := r.store.userRoles[userID]; ok {
		delete(roles, roleID)
	}
	return nil
}

// RemoveFromUserIfNotLast atomically removes the admin role from a user only
// when removing the assignment would not leave the system with zero effective
// admins. Two short-circuits mirror the PG removeIfNotLastSQL path and the
// migration-024 trigger semantics (S4.0):
//
//  1. If the target user is not currently active (locked / suspended),
//     removing their admin assignment cannot reduce the effective-admin set
//     (they were never counted), so the removal proceeds without a peer
//     check. The migration-024 trigger does the same: when
//     user_was_active_admin=false, the trigger RETURNs OLD without taking the
//     advisory lock.
//
//  2. Otherwise the target IS an effective admin and the removal would only
//     be safe if at least one OTHER effective admin remains after the
//     revoke.
//
// Non-admin roles are removed unconditionally (matches the trigger scope
// `IF OLD.role_id <> 'admin' THEN RETURN OLD;`).
//
// Holds the store write lock for both the read and the removal so the check
// is TOCTOU-free across users + role_assignments — mirrors the PG
// advisory-lock + FOR UPDATE OF u serialization.
func (r *RoleRepository) RemoveFromUserIfNotLast(_ context.Context, userID, roleID string) (bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	// Check if user actually holds the role.
	userHoldsRole := false
	if roles, ok := r.store.userRoles[userID]; ok {
		_, userHoldsRole = roles[roleID]
	}
	if !userHoldsRole {
		// Revoking a role the user does not hold is an idempotent no-op —
		// changed=false so the caller does not publish a role-change fact.
		return false, nil
	}

	if roleID == auth.RoleAdmin {
		// Short-circuit 1: target non-active → removal can never demote the
		// effective-admin count (the user wasn't counted). Aligns with
		// migration-024 trigger's user_was_active_admin=false branch.
		targetIsActive := false
		if u, ok := r.store.usersByID[userID]; ok {
			targetIsActive = u.Status() == domain.StatusActive
		}
		if targetIsActive {
			// Short-circuit 2: target IS effective admin — require at least
			// one OTHER effective admin to remain.
			if r.countOtherEffectiveAdminsLocked(userID) == 0 {
				// Removing this admin would leave zero effective admins. Same
				// errcode as the PG CTE detect path and migration-024 trigger
				// path so client handlers match a single business invariant.
				return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
					"cannot revoke admin: removing this assignment would leave the system with no effective admin; assign admin to an active user first",
					errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)))
			}
		}
	}

	delete(r.store.userRoles[userID], roleID)
	return true, nil
}

// ListByUserID returns paginated roles for userID sorted per params.
func (r *RoleRepository) ListByUserID(_ context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	roles := r.rolesByUserSnapshot(userID)

	query.Sort(roles, params.Sort, compareRoleField)
	result, err := query.ApplyCursor(roles, params, roleFieldValue)
	if err != nil {
		return nil, fmt.Errorf("role-repo: list-by-user: %w", err)
	}
	return result, nil
}

func (r *RoleRepository) rolesByUserSnapshot(userID string) []*domain.Role {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	roleIDs, ok := r.store.userRoles[userID]
	if !ok {
		return []*domain.Role{}
	}

	roles := make([]*domain.Role, 0, len(roleIDs))
	for rid := range roleIDs {
		if role, ok := r.store.roles[rid]; ok {
			clone := *role
			clone.Permissions = make([]domain.Permission, len(role.Permissions))
			copy(clone.Permissions, role.Permissions)
			roles = append(roles, &clone)
		}
	}
	return roles
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

// CountByRole returns the total count of role_assignments for roleID,
// regardless of user status. Used for bootstrap idempotency
// (adminprovision); MUST NOT be used as the last-admin invariant counter —
// see CountEffectiveAdmins.
func (r *RoleRepository) CountByRole(_ context.Context, roleID string) (int, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	count := 0
	for _, roleIDs := range r.store.userRoles {
		if _, ok := roleIDs[roleID]; ok {
			count++
		}
	}
	return count, nil
}

// CountEffectiveAdmins returns the number of users that are simultaneously
// status='active' AND hold the admin role. Satisfies the domain.
// EffectiveAdminCounter sealed interface (S4.0 invariant counter).
//
// Held under the shared store RLock so the user.status read and the
// role_assignments read are atomic — mirrors PG's advisory xact lock +
// FOR UPDATE OF u serialization.
func (r *RoleRepository) CountEffectiveAdmins(_ context.Context) (int, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	count := 0
	for userID, roleIDs := range r.store.userRoles {
		if _, hasAdmin := roleIDs[auth.RoleAdmin]; !hasAdmin {
			continue
		}
		u, ok := r.store.usersByID[userID]
		if !ok {
			// Role assignment refers to a user that no longer exists; in
			// production this is prevented by FK ON DELETE CASCADE in
			// migration 019. Treat as not-effective to be conservative.
			continue
		}
		if u.Status() == domain.StatusActive {
			count++
		}
	}
	return count, nil
}

// EffectiveAdminExists implements ports.RoleRepository — see the port godoc
// for fast-path semantics. RLock-only, returns true on the first match
// (constant-time-ish for typical small user sets).
func (r *RoleRepository) EffectiveAdminExists(_ context.Context) (bool, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	for userID, roleIDs := range r.store.userRoles {
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
// admin role) EXCLUDING excludeUserID. Caller MUST already hold store.mu
// (write or read).
func (r *RoleRepository) countOtherEffectiveAdminsLocked(excludeUserID string) int {
	count := 0
	for userID, roleIDs := range r.store.userRoles {
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
