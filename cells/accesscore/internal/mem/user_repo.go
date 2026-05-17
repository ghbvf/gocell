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

var _ ports.UserRepository = (*UserRepository)(nil)

const (
	msgUserNotFound   = "user not found"
	errMsgUsernameFmt = "username=%q"
	errMsgIDFmt       = "id=%q"
)

// UserRepository is the in-memory implementation of ports.UserRepository.
// It is always vended by Store.UserRepository() so the shared mutex covers
// any cross-repo invariant (e.g. effective-admin checks in RoleRepository).
//
// # Lock contract
//
// Methods on UserRepository follow the single-lock rule (see store.go package
// godoc). Each method checks r.store.txHoldsLock(ctx):
//
//   - txHoldsLock==true: store.mu is already held by memTxRunner.RunInTx on
//     the calling goroutine — do NOT acquire store.mu (sync.Mutex is not
//     reentrant; re-acquiring would deadlock).
//   - txHoldsLock==false (no token / WithTxContext / foreign store): acquire
//     store.mu for the duration of this method call.
//
// ForUpdate variants (GetByIDForUpdate, GetByUsernameForUpdate) follow the same
// rule: inside memTxRunner.RunInTx they read under the held store.mu and deliver
// SELECT FOR UPDATE-until-commit serialization; driven by a foreign CellTxManager
// (corebundle PG-outbox topology, ssobff/demo) they fall back to a per-call
// store.mu read lock — functional, but the cross-statement serialization
// guarantee holds only when the mem Store's own TxRunner drives the tx. PG is
// the production path that provides the hard guarantee unconditionally.
type UserRepository struct {
	store *Store
}

// Create persists a new User. Safe to call both inside and outside a RunInTx
// closure; see UserRepository lock contract.
func (r *UserRepository) Create(ctx context.Context, user *domain.User) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	if _, exists := r.store.byName[user.Username]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
			errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, user.Username)))
	}

	c := cloneUser(user)
	r.store.usersByID[user.ID] = c
	r.store.byName[user.Username] = c
	return nil
}

// GetByID returns the User with the given ID. Safe to call both inside and
// outside a RunInTx closure; see UserRepository lock contract.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.usersByID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, id)))
	}
	return cloneUser(u), nil
}

// GetByUsername returns the User with the given username. Safe to call both
// inside and outside a RunInTx closure; see UserRepository lock contract.
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.byName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, username)))
	}
	return cloneUser(u), nil
}

// GetByIDForUpdate (S4d): mem implementation of SELECT ... FOR UPDATE
// semantics. The mem store has no per-row lock distinct from GetByID — its
// serialization unit is the whole memTxRunner.RunInTx closure holding store.mu
// (full FOR-UPDATE-until-commit), or a per-call store.mu read under a foreign
// CellTxManager. Both behaviors are exactly GetByID's lock contract, so this
// is a deliberate, documented delegation: the ForUpdate vs plain distinction
// is a PG-only concept the port preserves; mem cannot and need not differ.
func (r *UserRepository) GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error) {
	return r.GetByID(ctx, id)
}

// GetByUsernameForUpdate (S4d): username-keyed counterpart to
// GetByIDForUpdate. Delegates to GetByUsername for the same reason — mem has
// no row lock distinct from the plain read (see GetByIDForUpdate doc).
func (r *UserRepository) GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error) {
	return r.GetByUsername(ctx, username)
}

// Update replaces the stored User. Safe to call both inside and outside a
// RunInTx closure; see UserRepository lock contract.
func (r *UserRepository) Update(ctx context.Context, user *domain.User) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.usersByID[user.ID]
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, user.ID)))
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
// already hold r.store.mu (either from the non-tx lock in Update, or because
// RunInTx holds it for the tx). Returns nil if the user does not hold the
// admin role at all.
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

// UpdatePassword applies a CAS-guarded password update. Safe to call both
// inside and outside a RunInTx closure; see UserRepository lock contract.
//
// If the stored PasswordVersion does not match expectedPV, it returns
// ErrVersionConflict (KindConflict / HTTP 409). On success it returns the new
// PasswordVersion (= expectedPV + 1).
func (r *UserRepository) UpdatePassword(
	ctx context.Context,
	userID string,
	newHash string,
	resetRequired bool,
	expectedPV int64,
) (int64, error) {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.usersByID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, userID)))
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
// user and returns the new value. Safe to call both inside and outside a
// RunInTx closure; see UserRepository lock contract.
//
// Returns ErrAuthUserNotFound when no user matches userID.
func (r *UserRepository) BumpAuthzEpoch(ctx context.Context, userID string) (int64, error) {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.usersByID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, userID)))
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

// Delete removes the User with the given ID. Safe to call both inside and
// outside a RunInTx closure; see UserRepository lock contract.
func (r *UserRepository) Delete(ctx context.Context, id string) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.usersByID[id]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, id)))
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
