package mem

import (
	"context"
	"fmt"
	"time"

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
	if _, exists := r.store.byEmail[user.Email]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "email already exists",
			errcode.WithInternal(fmt.Sprintf("email=%q", user.Email)))
	}

	c := cloneUser(user)
	r.store.usersByID[user.ID] = c
	r.store.byName[user.Username] = c
	r.store.byEmail[user.Email] = c
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

// UpdateProfile writes username / email / updated_at only. PATCH semantics:
// nil name/email skips that column. Returns the reconstituted *domain.User.
// Safe to call both inside and outside a RunInTx closure; see UserRepository
// lock contract.
func (r *UserRepository) UpdateProfile(
	ctx context.Context,
	userID string,
	name, email *domain.NonEmpty,
	now time.Time,
) (*domain.User, error) {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.usersByID[userID]
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, userID)))
	}

	newName := existing.Username
	if name != nil {
		newName = string(*name)
	}
	newEmail := existing.Email
	if email != nil {
		newEmail = string(*email)
	}

	// Uniqueness check mirrors PG (users.username UNIQUE / users.email UNIQUE).
	// Self-match (collider.ID == userID) is allowed so a same-value PATCH is
	// a legal no-op rather than spurious 409.
	if newName != existing.Username {
		if collider, hit := r.store.byName[newName]; hit && collider.ID != userID {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
				errcode.WithInternal(fmt.Sprintf(errMsgUsernameFmt, newName)))
		}
	}
	if newEmail != existing.Email {
		if collider, hit := r.store.byEmail[newEmail]; hit && collider.ID != userID {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "email already exists",
				errcode.WithInternal(fmt.Sprintf("email=%q", newEmail)))
		}
	}

	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    existing.ID,
		Username:              newName,
		Email:                 newEmail,
		PasswordHash:          existing.PasswordHash,
		PasswordVersion:       existing.PasswordVersion,
		PasswordResetRequired: existing.PasswordResetRequired(),
		Status:                existing.Status(),
		Source:                existing.CreationSource,
		AuthzEpoch:            existing.AuthzEpoch(),
		CreatedAt:             existing.CreatedAt,
		UpdatedAt:             now,
		FailedLoginCount:      existing.FailedLoginCount(),
		LastFailedAt:          copyTime(existing.LastFailedAt()),
		LockedUntil:           copyTime(existing.AutoLockoutDeadline()),
	})
	if err != nil {
		return nil, fmt.Errorf("mem: update-profile reconstitute: %w", err)
	}

	r.store.usersByID[userID] = updated
	if newName != existing.Username {
		delete(r.store.byName, existing.Username)
	}
	r.store.byName[newName] = updated
	if newEmail != existing.Email {
		delete(r.store.byEmail, existing.Email)
	}
	r.store.byEmail[newEmail] = updated
	return cloneUser(updated), nil
}

// UpdateLockState writes status + updated_at, and atomically zeros the
// auto-lockout columns when status == StatusActive (mirrors the SQL CASE
// logic in the PG adapter and ActivateUser.apply ResetFailedLogins call).
// Safe to call both inside and outside a RunInTx closure; see UserRepository
// lock contract.
func (r *UserRepository) UpdateLockState(
	ctx context.Context,
	userID string,
	status domain.UserStatus,
	now time.Time,
) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.usersByID[userID]
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, userID)))
	}

	// S4.0 effective-admin invariant safety net (parallels migration 024
	// effective_admin_invariant_on_users BEFORE UPDATE trigger). When a
	// status transition demotes an active admin (active → non-active) and
	// the user holds the admin role, refuse if no other effective admin
	// remains. Running inside the same write lock as the map mutation
	// matches the PG trigger's BEFORE-row semantics.
	if existing.Status() == domain.StatusActive && status != domain.StatusActive {
		if err := r.guardEffectiveAdminRemovalLocked(userID); err != nil {
			return err
		}
	}

	var failedLoginCount int
	var lastFailedAt *time.Time
	var lockedUntil *time.Time
	if status == domain.StatusActive {
		// Atomic lockout reset mirrors SQL CASE WHEN new.status='active' THEN 0/NULL/NULL.
		failedLoginCount = 0
		lastFailedAt = nil
		lockedUntil = nil
	} else {
		failedLoginCount = existing.FailedLoginCount()
		lastFailedAt = copyTime(existing.LastFailedAt())
		lockedUntil = copyTime(existing.AutoLockoutDeadline())
	}

	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    existing.ID,
		Username:              existing.Username,
		Email:                 existing.Email,
		PasswordHash:          existing.PasswordHash,
		PasswordVersion:       existing.PasswordVersion,
		PasswordResetRequired: existing.PasswordResetRequired(),
		Status:                status,
		Source:                existing.CreationSource,
		AuthzEpoch:            existing.AuthzEpoch(),
		CreatedAt:             existing.CreatedAt,
		UpdatedAt:             now,
		FailedLoginCount:      failedLoginCount,
		LastFailedAt:          lastFailedAt,
		LockedUntil:           lockedUntil,
	})
	if err != nil {
		return fmt.Errorf("mem: update-lock-state reconstitute: %w", err)
	}

	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	r.store.byEmail[updated.Email] = updated
	return nil
}

// UpdatePasswordResetFlag writes password_reset_required + updated_at only.
// Safe to call both inside and outside a RunInTx closure; see UserRepository
// lock contract.
func (r *UserRepository) UpdatePasswordResetFlag(
	ctx context.Context,
	userID string,
	required bool,
	now time.Time,
) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.usersByID[userID]
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, userID)))
	}

	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    existing.ID,
		Username:              existing.Username,
		Email:                 existing.Email,
		PasswordHash:          existing.PasswordHash,
		PasswordVersion:       existing.PasswordVersion,
		PasswordResetRequired: required,
		Status:                existing.Status(),
		Source:                existing.CreationSource,
		AuthzEpoch:            existing.AuthzEpoch(),
		CreatedAt:             existing.CreatedAt,
		UpdatedAt:             now,
		FailedLoginCount:      existing.FailedLoginCount(),
		LastFailedAt:          copyTime(existing.LastFailedAt()),
		LockedUntil:           copyTime(existing.AutoLockoutDeadline()),
	})
	if err != nil {
		return fmt.Errorf("mem: update-password-reset-flag reconstitute: %w", err)
	}

	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	r.store.byEmail[updated.Email] = updated
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
		FailedLoginCount:      u.FailedLoginCount(),
		LastFailedAt:          copyTime(u.LastFailedAt()),
		LockedUntil:           copyTime(u.AutoLockoutDeadline()),
	})
	if err != nil {
		// ReconstituteUser only fails on invalid values; a well-formed stored
		// User cannot trigger this. Panic to surface corrupt store state early.
		panic(panicregister.Approved("mem-clone-user-invalid-stored",
			errcode.Assertion("mem: cloneUser: unexpected invalid stored User: %v", err)))
	}
	return clone
}

// copyTime returns a fresh pointer to the same time value (or nil for nil
// input). Without this, two stored users could share the same *time.Time
// pointer; mutating one would surface in the other.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
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
		FailedLoginCount:      u.FailedLoginCount(),
		LastFailedAt:          copyTime(u.LastFailedAt()),
		LockedUntil:           copyTime(u.AutoLockoutDeadline()),
	})
	if err != nil {
		return 0, fmt.Errorf("mem: update-password reconstitute: %w", err)
	}
	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	r.store.byEmail[updated.Email] = updated
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
		FailedLoginCount:      u.FailedLoginCount(),
		LastFailedAt:          copyTime(u.LastFailedAt()),
		LockedUntil:           copyTime(u.AutoLockoutDeadline()),
	})
	if err != nil {
		return 0, fmt.Errorf("mem: bump-authz-epoch reconstitute: %w", err)
	}
	r.store.usersByID[userID] = updated
	r.store.byName[updated.Username] = updated
	r.store.byEmail[updated.Email] = updated
	return newEpoch, nil
}

// UpdateLockoutFields persists the auto-lockout state (failed_login_count,
// last_failed_at, locked_until) for an existing user. Safe to call both
// inside and outside a RunInTx closure; see UserRepository lock contract.
//
// Implementation note: the mem store re-clones the entire user via
// ReconstituteUser, carrying over every field — including status and
// authz_epoch. The accountlockout service contract is that it does NOT
// mutate status/epoch through this method (those routes are reserved for
// authzmutate.Mutator.ApplyInTx); the user's in-memory state at the time of
// the call must reflect any pending status/epoch changes (which, in the
// expected call order, are absent — sessionlogin reads user, calls
// UpdateLockoutFields, then may call ApplyInTx(LockUser) which goes through
// a separate Update path).
func (r *UserRepository) UpdateLockoutFields(ctx context.Context, user *domain.User) error {
	if !r.store.txHoldsLock(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	if _, exists := r.store.usersByID[user.ID]; !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf(errMsgIDFmt, user.ID)))
	}

	c := cloneUser(user)
	r.store.usersByID[user.ID] = c
	r.store.byName[user.Username] = c
	r.store.byEmail[user.Email] = c
	return nil
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
	delete(r.store.byEmail, u.Email)
	delete(r.store.usersByID, id)
	// Cascade: drop the user's role assignments — mirrors the PG
	// `role_assignments.user_id REFERENCES users(id) ON DELETE CASCADE` FK in
	// migration 019. Without this, mem leaks stale role rows that would
	// otherwise be visible to CountEffectiveAdmins for a deleted user.
	delete(r.store.userRoles, id)
	return nil
}
