package mem

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ ports.RoleRepository = (*RoleRepository)(nil)

// RoleRepository is an in-memory implementation of ports.RoleRepository.
type RoleRepository struct {
	mu        sync.RWMutex
	roles     map[string]*domain.Role        // roleID -> role
	userRoles map[string]map[string]struct{} // userID -> set of roleIDs
}

// NewRoleRepository creates an empty in-memory RoleRepository.
func NewRoleRepository() *RoleRepository {
	return &RoleRepository{
		roles:     make(map[string]*domain.Role),
		userRoles: make(map[string]map[string]struct{}),
	}
}

// SeedRole adds a role for testing purposes.
func (r *RoleRepository) SeedRole(role *domain.Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	r.roles[role.ID] = &clone
}

// Create persists a new role. Idempotent: if a role with the same ID already
// exists, it is silently overwritten (upsert semantics for seed/bootstrap).
func (r *RoleRepository) Create(_ context.Context, role *domain.Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *role
	clone.Permissions = make([]domain.Permission, len(role.Permissions))
	copy(clone.Permissions, role.Permissions)
	r.roles[role.ID] = &clone
	return nil
}

func (r *RoleRepository) GetByID(_ context.Context, id string) (*domain.Role, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	role, ok := r.roles[id]
	if !ok {
		return nil, errcode.New(errcode.ErrAuthRoleNotFound, "role not found: "+id)
	}
	clone := *role
	return &clone, nil
}

func (r *RoleRepository) GetByUserID(_ context.Context, userID string) ([]*domain.Role, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	roleIDs, ok := r.userRoles[userID]
	if !ok {
		return []*domain.Role{}, nil
	}

	var result []*domain.Role
	for rid := range roleIDs {
		if role, ok := r.roles[rid]; ok {
			clone := *role
			result = append(result, &clone)
		}
	}
	return result, nil
}

func (r *RoleRepository) AssignToUser(_ context.Context, userID, roleID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.roles[roleID]; !ok {
		return false, errcode.New(errcode.ErrAuthRoleNotFound, "role not found: "+roleID)
	}

	if r.userRoles[userID] == nil {
		r.userRoles[userID] = make(map[string]struct{})
	}
	if _, already := r.userRoles[userID][roleID]; already {
		return false, nil
	}
	r.userRoles[userID][roleID] = struct{}{}
	return true, nil
}

func (r *RoleRepository) RemoveFromUser(_ context.Context, userID, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if roles, ok := r.userRoles[userID]; ok {
		delete(roles, roleID)
	}
	return nil
}

// RemoveFromUserIfNotLast atomically removes the role from the user only if
// at least one other holder will remain. Holds the write lock for both the
// count check and the removal to eliminate TOCTOU races.
func (r *RoleRepository) RemoveFromUserIfNotLast(_ context.Context, userID, roleID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if user actually holds the role.
	userHoldsRole := false
	if roles, ok := r.userRoles[userID]; ok {
		_, userHoldsRole = roles[roleID]
	}

	// Revoking a role the user does not hold is an idempotent no-op — return
	// changed=false so the caller does not publish a role-change fact.
	if !userHoldsRole {
		return false, nil
	}

	// Count holders under the same lock.
	count := 0
	for _, roleIDs := range r.userRoles {
		if _, ok := roleIDs[roleID]; ok {
			count++
		}
	}

	if count == 1 {
		return false, errcode.New(errcode.ErrAuthForbidden,
			fmt.Sprintf("cannot revoke role %q from user %q: this is the only holder; assign the role to another user first", roleID, userID))
	}

	delete(r.userRoles[userID], roleID)
	return true, nil
}

// CountByRole returns the number of users assigned to the given role.
func (r *RoleRepository) CountByRole(_ context.Context, roleID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, roleIDs := range r.userRoles {
		if _, ok := roleIDs[roleID]; ok {
			count++
		}
	}
	return count, nil
}
