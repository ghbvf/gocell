package accesscoretest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// FakeRoleRepo implements ports.RoleRepository in memory for unit tests.
// All operations are concurrency-safe.
type FakeRoleRepo struct {
	mu    sync.Mutex
	roles map[string]*domain.Role    // roleID → Role
	assns map[string]map[string]bool // userID → set of roleIDs
	calls []FakeCall
}

// NewFakeRoleRepo returns an empty FakeRoleRepo.
func NewFakeRoleRepo() *FakeRoleRepo {
	return &FakeRoleRepo{
		roles: make(map[string]*domain.Role),
		assns: make(map[string]map[string]bool),
	}
}

// SeedAssignment records that userID holds roleID. It also auto-creates a minimal
// Role stub with the given roleID if one does not yet exist.
func (r *FakeRoleRepo) SeedAssignment(userID, roleID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.roles[roleID]; !ok {
		r.roles[roleID] = &domain.Role{ID: roleID, Name: roleID}
	}
	if r.assns[userID] == nil {
		r.assns[userID] = make(map[string]bool)
	}
	r.assns[userID][roleID] = true
}

// Snapshot returns a map of userID → []roleID. The map and slices are copies.
func (r *FakeRoleRepo) Snapshot() map[string][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]string, len(r.assns))
	for uid, set := range r.assns {
		roles := make([]string, 0, len(set))
		for rid := range set {
			roles = append(roles, rid)
		}
		out[uid] = roles
	}
	return out
}

// CallsOf returns all recorded calls for the given method name.
func (r *FakeRoleRepo) CallsOf(method string) []FakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []FakeCall
	for _, c := range r.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (r *FakeRoleRepo) recordRole(method string, args ...string) {
	r.calls = append(r.calls, FakeCall{Method: method, Args: args})
}

// GetByID returns the role with the given ID. Returns ErrAuthRoleNotFound when absent.
func (r *FakeRoleRepo) GetByID(_ context.Context, id string) (*domain.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("GetByID", id)
	role, ok := r.roles[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found")
	}
	return cloneRole(role), nil
}

// GetByUserID returns all roles assigned to userID.
func (r *FakeRoleRepo) GetByUserID(_ context.Context, userID string) ([]*domain.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("GetByUserID", userID)
	set := r.assns[userID]
	out := make([]*domain.Role, 0, len(set))
	for rid := range set {
		if role, ok := r.roles[rid]; ok {
			out = append(out, cloneRole(role))
		}
	}
	return out, nil
}

// Create stores the role. Overwrites any existing role with the same ID.
func (r *FakeRoleRepo) Create(_ context.Context, role *domain.Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("Create", role.ID)
	r.roles[role.ID] = cloneRole(role)
	return nil
}

// AssignToUser idempotently assigns roleID to userID.
// changed=true when the assignment is new; changed=false when it already existed.
func (r *FakeRoleRepo) AssignToUser(_ context.Context, userID, roleID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("AssignToUser", userID, roleID)
	if r.assns[userID] == nil {
		r.assns[userID] = make(map[string]bool)
	}
	if r.assns[userID][roleID] {
		return false, nil
	}
	r.assns[userID][roleID] = true
	return true, nil
}

// RemoveFromUser idempotently removes roleID from userID.
func (r *FakeRoleRepo) RemoveFromUser(_ context.Context, userID, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("RemoveFromUser", userID, roleID)
	if r.assns[userID] != nil {
		delete(r.assns[userID], roleID)
	}
	return nil
}

// RemoveFromUserIfNotLast removes a role assignment with admin-scoped last-effective-admin
// protection. For non-admin roles it is a plain idempotent delete.
func (r *FakeRoleRepo) RemoveFromUserIfNotLast(_ context.Context, userID, roleID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("RemoveFromUserIfNotLast", userID, roleID)
	set := r.assns[userID]
	if set == nil || !set[roleID] {
		return false, nil
	}
	// Admin protection: refuse if this is the last effective admin.
	if roleID == auth.RoleAdmin {
		effective := r.countEffectiveAdminsLocked()
		if effective <= 1 {
			return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove last effective admin")
		}
	}
	delete(set, roleID)
	return true, nil
}

// CountByRole counts all role_assignments for roleID, regardless of user status.
func (r *FakeRoleRepo) CountByRole(_ context.Context, roleID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("CountByRole", roleID)
	count := 0
	for _, set := range r.assns {
		if set[roleID] {
			count++
		}
	}
	return count, nil
}

// CountEffectiveAdmins counts users who hold the admin role.
// The fake does not track user status, so it counts all users with the admin role
// (conservative approximation sufficient for unit tests that do not test the
// status-filter invariant — integration tests use the PG implementation).
func (r *FakeRoleRepo) CountEffectiveAdmins(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("CountEffectiveAdmins")
	return r.countEffectiveAdminsLocked(), nil
}

func (r *FakeRoleRepo) countEffectiveAdminsLocked() int {
	count := 0
	for _, set := range r.assns {
		if set[auth.RoleAdmin] {
			count++
		}
	}
	return count
}

// EffectiveAdminExists returns true when at least one user holds the admin role.
func (r *FakeRoleRepo) EffectiveAdminExists(_ context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("EffectiveAdminExists")
	return r.countEffectiveAdminsLocked() > 0, nil
}

// ListByUserID returns all roles for userID as a flat slice (pagination ignored).
func (r *FakeRoleRepo) ListByUserID(_ context.Context, userID string, _ query.ListParams) ([]*domain.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordRole("ListByUserID", userID)
	set := r.assns[userID]
	out := make([]*domain.Role, 0, len(set))
	for rid := range set {
		if role, ok := r.roles[rid]; ok {
			out = append(out, cloneRole(role))
		}
	}
	return out, nil
}

// compile-time interface check.
var _ ports.RoleRepository = (*FakeRoleRepo)(nil)

func cloneRole(r *domain.Role) *domain.Role {
	cloned := &domain.Role{
		ID:          r.ID,
		Name:        r.Name,
		Permissions: make([]domain.Permission, len(r.Permissions)),
	}
	copy(cloned.Permissions, r.Permissions)
	return cloned
}
