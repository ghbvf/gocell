package mem

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

var _ ports.UserRepository = (*UserRepository)(nil)

const msgUserNotFound = "user not found"

// UserRepository is the in-memory implementation of ports.UserRepository.
// It is always vended by Store.UserRepository() so the shared mutex covers
// any cross-repo invariant (e.g. effective-admin checks in RoleRepository).
type UserRepository struct {
	store *Store
}

func (r *UserRepository) Create(_ context.Context, user *domain.User) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	if _, exists := r.store.byName[user.Username]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
			errcode.WithInternal(fmt.Sprintf("username=%q", user.Username)))
	}

	c := cloneUser(user)
	r.store.usersByID[user.ID] = c
	r.store.byName[user.Username] = c
	return nil
}

func (r *UserRepository) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()

	u, ok := r.store.usersByID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	return cloneUser(u), nil
}

func (r *UserRepository) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()

	u, ok := r.store.byName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("username=%q", username)))
	}
	return cloneUser(u), nil
}

func (r *UserRepository) Update(_ context.Context, user *domain.User) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	existing, exists := r.store.usersByID[user.ID]
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", user.ID)))
	}

	// S4.0 effective-admin invariant safety net (parallels migration 024
	// effective_admin_invariant_on_users BEFORE UPDATE trigger). When a
	// status transition demotes an active admin (active → non-active) and
	// the user holds the admin role, refuse if no other effective admin
	// remains. Kept inline so the mem path mirrors PG's atomic-with-mutation
	// check; running it inside the same write lock as the map mutation
	// matches the PG trigger's BEFORE-row semantics.
	if existing.Status == domain.StatusActive && user.Status != domain.StatusActive {
		if err := r.guardEffectiveAdminRemovalLocked(user.ID); err != nil {
			return err
		}
	}

	c := cloneUser(user)
	r.store.usersByID[user.ID] = c
	r.store.byName[user.Username] = c
	return nil
}

// guardEffectiveAdminRemovalLocked refuses the in-progress mutation when
// removing/demoting userID would leave zero effective admins. Caller MUST
// already hold r.store.mu (write lock). Returns nil if the user does not
// hold the admin role at all.
func (r *UserRepository) guardEffectiveAdminRemovalLocked(userID string) error {
	roles, hasRoles := r.store.userRoles[userID]
	if !hasRoles {
		return nil
	}
	if _, hasAdmin := roles["admin"]; !hasAdmin {
		return nil
	}
	// User is admin AND currently active. Count OTHER effective admins.
	other := 0
	for otherID, otherRoles := range r.store.userRoles {
		if otherID == userID {
			continue
		}
		if _, ok := otherRoles["admin"]; !ok {
			continue
		}
		u, ok := r.store.usersByID[otherID]
		if !ok {
			continue
		}
		if u.Status == domain.StatusActive {
			other++
		}
	}
	if other == 0 {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
			"cannot remove the last effective admin",
			errcode.WithCategory(errcode.CategoryAuth),
			errcode.WithInternal(fmt.Sprintf("user_id=%q", userID)))
	}
	return nil
}

// cloneUser creates a deep copy of a User to avoid sharing pointers across map entries.
func cloneUser(u *domain.User) *domain.User {
	return &domain.User{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordVersion:       u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired,
		Status:                u.Status,
		CreationSource:        u.CreationSource,
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
	}
}

// UpdatePassword applies a CAS-guarded password update.
//
// If the stored PasswordVersion does not match expectedPV, it returns
// ErrVersionConflict (KindConflict / HTTP 409). On success it returns the new
// PasswordVersion (= expectedPV + 1).
func (r *UserRepository) UpdatePassword(
	_ context.Context,
	userID string,
	newHash string,
	resetRequired bool,
	expectedPV int64,
) (int64, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	u, ok := r.store.usersByID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", userID)))
	}
	if u.PasswordVersion != expectedPV {
		return 0, cas.CheckVersionMatch(0, "user", userID)
	}
	u.PasswordHash = newHash
	u.PasswordResetRequired = resetRequired
	u.PasswordVersion++
	u.UpdatedAt = r.store.clock.Now()
	return u.PasswordVersion, nil
}

func (r *UserRepository) Delete(_ context.Context, id string) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	u, ok := r.store.usersByID[id]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}

	// S4.0 effective-admin invariant safety net (parallels migration 024
	// effective_admin_invariant_on_users BEFORE DELETE trigger). Deleting an
	// active admin removes them from the effective-admin set; refuse if no
	// other effective admin remains.
	if u.Status == domain.StatusActive {
		if err := r.guardEffectiveAdminRemovalLocked(id); err != nil {
			return err
		}
	}

	delete(r.store.byName, u.Username)
	delete(r.store.usersByID, id)
	// Cascade: drop the user's role assignments — mirrors the PG
	// `role_assignments.user_id REFERENCES users(id) ON DELETE CASCADE` FK in
	// migration 019. Without this, mem leaks stale role rows that would
	// otherwise be visible to CountEffectiveAdmins for a deleted user.
	delete(r.store.userRoles, id)
	return nil
}
