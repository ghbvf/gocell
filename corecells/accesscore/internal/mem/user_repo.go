package mem

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

var _ ports.UserRepository = (*UserRepository)(nil)

const (
	msgUserNotFound      = "user not found"
	msgUserInvalidTenant = "user_repo: invalid tenant"
	errMsgUsernameFmt    = "username=%q"
	errMsgIDFmt          = "id=%q"
)

// UserRepository is the in-memory implementation of ports.UserRepository.
// It is always vended by Store.UserRepository() so the shared mutex covers
// any cross-repo invariant (e.g. effective-admin checks in RoleRepository).
//
// # Tenancy (#1337 PR-2 + PR-3b)
//
// Methods take a mandatory tenant.TenantID positional parameter and scope all
// reads/writes to the tenant's partition of the underlying maps. After PR-3b
// the tenant-less GetByID carve-out is removed; all by-PK reads are tenant-scoped.
//
// # Lock contract
//
// Methods on UserRepository follow the single-lock rule (see store.go package
// godoc). Each method checks r.store.inLiveTx(ctx):
//
//   - inLiveTx==true: store.mu is already held by memTxRunner.RunInTx on
//     the calling goroutine (a live lease is in ctx) — do NOT acquire store.mu
//     (sync.Mutex is not reentrant; re-acquiring would deadlock).
//   - inLiveTx==false (no lease / dead lease / foreign store): acquire
//     store.mu for the duration of this method call.
type UserRepository struct {
	store *Store
}

// Create persists a new User within the tenant. Safe to call both inside and
// outside a RunInTx closure; see UserRepository lock contract.
func (r *UserRepository) Create(ctx context.Context, t tenant.TenantID, user *domain.User) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))

	if _, exists := tByName[user.Username]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgUsernameFmt, user.Username))))
	}
	if _, exists := tByEmail[user.Email]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "email already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("email=%q", user.Email))))
	}

	c := cloneUser(user)
	// Stamp the tenant authoritatively from the Create param (not the input
	// aggregate) so every later READ (GetByID returns this stored copy) carries
	// the tenant — the source for the by-PK tenant-deriving path (rbacassign).
	c.TenantID = t
	r.store.usersByID[user.ID] = c
	tByName[user.Username] = c
	tByEmail[user.Email] = c
	return nil
}

// checkProfileUniqueLocked enforces the tenant-scoped username / email UNIQUE
// constraint for UpdateProfile (mirrors PG). Caller must hold the store lock.
// Returns ErrAuthUserDuplicate when a different user in the tenant already holds
// the target username or email.
func checkProfileUniqueLocked(tByName, tByEmail map[string]*domain.User, userID, newName, newEmail string, existing *domain.User) error {
	if newName != existing.Username {
		if collider, hit := tByName[newName]; hit && collider.ID != userID {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgUsernameFmt, newName))))
		}
	}
	if newEmail != existing.Email {
		if collider, hit := tByEmail[newEmail]; hit && collider.ID != userID {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "email already exists",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("email=%q", newEmail))))
		}
	}
	return nil
}

// GetByIDInTenant fetches a user by primary key and verifies it belongs to t.
// Returns ErrAuthUserNotFound when the row is absent OR in a different tenant,
// collapsing both cases to prevent cross-tenant existence enumeration.
func (r *UserRepository) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.userByIDInTenant(id, string(t))
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, id))))
	}
	return cloneUser(existing), nil
}

// GetByUsername returns the User with the given username within the tenant.
// Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) GetByUsername(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	tByName := r.store.tenantByName(string(t))
	u, ok := tByName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgUsernameFmt, username))))
	}
	return cloneUser(u), nil
}

// GetByIDForUpdate (S4d): mem implementation of SELECT ... FOR UPDATE
// semantics. Tenant-scoped: callers are post-auth and carry a tenant. After
// PR-3b this delegates directly to GetByIDInTenant (the former GetByID
// carve-out is removed). The mem store serializes via store.mu held in RunInTx
// — for details see UserRepository lock contract.
func (r *UserRepository) GetByIDForUpdate(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	return r.GetByIDInTenant(ctx, t, id)
}

// GetByUsernameForUpdate (S4d): username-keyed counterpart to
// GetByIDForUpdate, scoped to the tenant. Delegates to GetByUsername for the
// same reason — mem has no row lock distinct from the plain read.
func (r *UserRepository) GetByUsernameForUpdate(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	return r.GetByUsername(ctx, t, username)
}

// UpdateProfile writes username / email / updated_at only within the tenant.
// PATCH semantics: nil name/email skips that column. Returns the reconstituted
// *domain.User. Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) UpdateProfile(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	name, email *domain.NonEmpty,
	now time.Time,
) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.userByIDInTenant(userID, string(t))
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
	}

	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))

	newName := existing.Username
	if name != nil {
		newName = string(*name)
	}
	newEmail := existing.Email
	if email != nil {
		newEmail = string(*email)
	}

	// Uniqueness check mirrors PG (tenant-scoped username / email UNIQUE).
	if err := checkProfileUniqueLocked(tByName, tByEmail, userID, newName, newEmail, existing); err != nil {
		return nil, err
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

	updated.TenantID = t
	r.store.usersByID[userID] = updated
	if newName != existing.Username {
		delete(tByName, existing.Username)
	}
	tByName[newName] = updated
	if newEmail != existing.Email {
		delete(tByEmail, existing.Email)
	}
	tByEmail[newEmail] = updated
	return cloneUser(updated), nil
}

// UpdateLockState writes status + updated_at within the tenant, and atomically
// zeros the auto-lockout columns when status == StatusActive.
// Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) UpdateLockState(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	status domain.UserStatus,
	now time.Time,
) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.userByIDInTenant(userID, string(t))
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
	}

	// S4.0 effective-admin invariant safety net (per-tenant).
	if existing.Status() == domain.StatusActive && status != domain.StatusActive {
		if err := r.guardEffectiveAdminRemovalLocked(string(t), userID); err != nil {
			return err
		}
	}

	var failedLoginCount int
	var lastFailedAt *time.Time
	var lockedUntil *time.Time
	if status == domain.StatusActive {
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

	updated.TenantID = t
	r.store.usersByID[userID] = updated
	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	tByName[updated.Username] = updated
	tByEmail[updated.Email] = updated
	return nil
}

// UpdatePasswordResetFlag writes password_reset_required + updated_at only
// within the tenant. Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) UpdatePasswordResetFlag(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	required bool,
	now time.Time,
) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	existing, exists := r.store.userByIDInTenant(userID, string(t))
	if !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
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

	updated.TenantID = t
	r.store.usersByID[userID] = updated
	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	tByName[updated.Username] = updated
	tByEmail[updated.Email] = updated
	return nil
}

// guardEffectiveAdminRemovalLocked refuses the in-progress mutation when
// removing/demoting userID would leave zero effective admins within the
// tenant. Caller MUST already hold r.store.mu.
func (r *UserRepository) guardEffectiveAdminRemovalLocked(tenantID, userID string) error {
	tUserRoles := r.store.tenantUserRoles(tenantID)
	roles, hasRoles := tUserRoles[userID]
	if !hasRoles {
		return nil
	}
	if _, hasAdmin := roles[auth.RoleAdmin]; !hasAdmin {
		return nil
	}
	// User is admin AND currently active. Count OTHER effective admins within tenant.
	other := 0
	for otherID, otherRoles := range tUserRoles {
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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("user_id=%q", userID))))
	}
	return nil
}

// cloneUser creates a deep copy of a User to avoid sharing pointers across map entries.
// Uses domain.ReconstituteUser so that private fields are faithfully copied.
func cloneUser(u *domain.User) *domain.User {
	clone, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		TenantID:              u.TenantID,
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

// UpdatePassword applies a CAS-guarded password update within the tenant.
// Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) UpdatePassword(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	newHash string,
	resetRequired bool,
	expectedPV int64,
) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.userByIDInTenant(userID, string(t))
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
	}
	// Status guard (#1017 F1): mirror the SQL `AND status='active'` predicate.
	if u.Status() != domain.StatusActive {
		return 0, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"account is not active",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
	}
	if u.PasswordVersion != expectedPV {
		return 0, cas.CheckVersionMatch(0, "user", userID)
	}
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
	updated.TenantID = t
	r.store.usersByID[userID] = updated
	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	tByName[updated.Username] = updated
	tByEmail[updated.Email] = updated
	return updated.PasswordVersion, nil
}

// BumpAuthzEpoch atomically increments the AuthzEpoch counter for the given
// user and returns the new value. Safe to call both inside and outside a
// RunInTx closure. The lookup is tenant-scoped via userByIDInTenant to prevent
// a cross-tenant epoch bump, mirroring the PG bumpAuthzEpochSQL tenant predicate.
func (r *UserRepository) BumpAuthzEpoch(
	ctx context.Context, t tenant.TenantID, userID string, tok credentialfence.FenceToken,
) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	credentialfence.MustHave(tok, "ports.UserRepository.BumpAuthzEpoch")
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.userByIDInTenant(userID, string(t))
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, userID))))
	}
	newEpoch := u.AuthzEpoch() + 1
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
	updated.TenantID = t
	r.store.usersByID[userID] = updated
	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	tByName[updated.Username] = updated
	tByEmail[updated.Email] = updated
	return newEpoch, nil
}

// UpdateLockoutFields persists the auto-lockout state for an existing user
// within the tenant. Safe to call both inside and outside a RunInTx closure.
func (r *UserRepository) UpdateLockoutFields(ctx context.Context, t tenant.TenantID, user *domain.User) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	if _, exists := r.store.userByIDInTenant(user.ID, string(t)); !exists {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, user.ID))))
	}

	c := cloneUser(user)
	c.TenantID = t // authoritative tenant from the param (input aggregate carries none)
	r.store.usersByID[user.ID] = c
	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	tByName[user.Username] = c
	tByEmail[user.Email] = c
	return nil
}

// Delete removes the User with the given ID within the tenant. Safe to call
// both inside and outside a RunInTx closure.
func (r *UserRepository) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	if !r.store.inLiveTx(ctx) {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
	}

	u, ok := r.store.userByIDInTenant(id, string(t))
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(errMsgIDFmt, id))))
	}

	// S4.0 effective-admin invariant safety net (per-tenant).
	if u.Status() == domain.StatusActive {
		if err := r.guardEffectiveAdminRemovalLocked(string(t), id); err != nil {
			return err
		}
	}

	tByName := r.store.tenantByName(string(t))
	tByEmail := r.store.tenantByEmail(string(t))
	delete(tByName, u.Username)
	delete(tByEmail, u.Email)
	delete(r.store.usersByID, id)
	// Cascade: drop the user's role assignments within the tenant — mirrors the PG
	// `role_assignments.user_id REFERENCES users(id) ON DELETE CASCADE` FK.
	tUserRoles := r.store.tenantUserRoles(string(t))
	delete(tUserRoles, id)
	return nil
}
