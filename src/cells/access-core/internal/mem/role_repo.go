package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)


var _ ports.RoleRepository = (*RoleRepository)(nil)

// RoleRepository is an in-memory implementation of ports.RoleRepository.
type RoleRepository struct {
	mu        sync.RWMutex
	roles     map[string]*domain.Role          // roleID -> role
	userRoles map[string]map[string]struct{}    // userID -> set of roleIDs
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
		return nil, nil
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

func (r *RoleRepository) AssignToUser(_ context.Context, userID, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.roles[roleID]; !ok {
		return errcode.New(errcode.ErrAuthRoleNotFound, "role not found: "+roleID)
	}

	if r.userRoles[userID] == nil {
		r.userRoles[userID] = make(map[string]struct{})
	}
	r.userRoles[userID][roleID] = struct{}{}
	return nil
}

func (r *RoleRepository) RemoveFromUser(_ context.Context, userID, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if roles, ok := r.userRoles[userID]; ok {
		delete(roles, roleID)
	}
	return nil
}
