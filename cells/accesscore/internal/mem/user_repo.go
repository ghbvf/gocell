package mem

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// assertMemTx returns an error when ctx does not carry the mem-tx sentinel
// injected by Store.TxRunner. FOR-UPDATE semantics require an enclosing
// RunInTx boundary — calling without one is a programming error.
func assertMemTx(ctx context.Context) error {
	if v, _ := ctx.Value(memTxKey{}).(bool); v {
		return nil
	}
	return errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"user_repo: FOR UPDATE requires a mem transaction context; call inside Store.TxRunner().RunInTx")
}

var _ ports.UserRepository = (*UserRepository)(nil)

const (
	msgUserNotFound   = "user not found"
	errMsgUsernameFmt = "username=%q"
)

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
			errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, user.Username)))
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
			errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, username)))
	}
	return cloneUser(u), nil
}

// GetByIDForUpdate (S4d): mem implementation fail-fasts when ctx does not
// carry the mem-tx sentinel from Store.TxRunner — matching assertAmbientTx
// in the PG adapter. When inside a valid RunInTx body it acquires the
// store-wide write lock, serializing against concurrent writes (BumpAuthzEpoch,
// RevokeForSubject, etc.) in the same way PG SELECT FOR UPDATE would.
//
// fail-fast enforced: calling without a mem-tx context returns errcode.ErrInternal.
func (r *UserRepository) GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error) {
	if err := assertMemTx(ctx); err != nil {
		return nil, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	u, ok := r.store.usersByID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	return cloneUser(u), nil
}

// GetByUsernameForUpdate (S4d): same fail-fast and locking semantics as
// GetByIDForUpdate. Requires the mem-tx sentinel from Store.TxRunner.
//
// fail-fast enforced: calling without a mem-tx context returns errcode.ErrInternal.
func (r *UserRepository) GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error) {
	if err := assertMemTx(ctx); err != nil {
		return nil, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	u, ok := r.store.byName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, username)))
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
	if existing.Status() == domain.StatusActive && user.Status() != domain.StatusActive {
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
	if _, hasAdmin := roles[auth.RoleAdmin]; !hasAdmin {
		return nil
	}
	// User is admin AND currently active. Count OTHER effective admins.
	other := 0
	for otherID, otherRoles := range r.store.userRoles {
		if otherID == userID {
			continue
		}
		if _, ok := otherRoles[auth.RoleAdmin]; !ok {
			continue
		}
		u, ok := r.store.usersByID[otherID]
		if !ok {
			continue
		}
		if u.Status() == domain.StatusActive {
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
// Uses domain.ReconstituteUser so that private fields are faithfully copied.
func cloneUser(u *domain.User) *domain.User {
	clone, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordVersion:       u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired(),
		Status:                u.Status(),
		Source:                u.CreationSource,
		AuthzEpoch:            u.AuthzEpoch(),
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
	})
	if err != nil {
		// ReconstituteUser only fails on invalid values; a well-formed stored
		// User cannot trigger this. Panic to surface corrupt store state early.
		panic(panicregister.Approved("mem-clone-user-invalid-stored",
			errcode.Assertion("mem: cloneUser: unexpected invalid stored User: %v", err)))
	}
	return clone
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
	// Rebuild via ReconstituteUser with updated fields so private fields are set correctly.
	now := r.store.clock.Now()
	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          newHash,
		PasswordVersion:       u.PasswordVersion + 1,
		PasswordResetRequired: resetRequired,
		Status:                u.Status(),
		Source:                u.CreationSource,
		AuthzEpoch:            u.AuthzEpoch(),
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             now,
	})
	if err != nil {
		return 0, fmt.Errorf("mem: update-password reconstitute: %w", err)
	}
	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	return updated.PasswordVersion, nil
}

// BumpAuthzEpoch atomically increments the AuthzEpoch counter for the given
// user and returns the new value. Returns ErrAuthUserNotFound when no user
// matches userID.
func (r *UserRepository) BumpAuthzEpoch(_ context.Context, userID string) (int64, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	u, ok := r.store.usersByID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", userID)))
	}
	newEpoch := u.AuthzEpoch() + 1
	// Rebuild the stored user with the bumped epoch.
	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordVersion:       u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired(),
		Status:                u.Status(),
		Source:                u.CreationSource,
		AuthzEpoch:            newEpoch,
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
	})
	if err != nil {
		return 0, fmt.Errorf("mem: bump-authz-epoch reconstitute: %w", err)
	}
	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	return newEpoch, nil
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
	if u.Status() == domain.StatusActive {
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
