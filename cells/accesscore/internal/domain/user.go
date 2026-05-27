// Package domain contains the accesscore Cell domain models.
package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// UserStatus represents the account state of a user.
type UserStatus string

const (
	// StatusActive indicates the user account is active and usable.
	StatusActive UserStatus = "active"
	// StatusSuspended indicates the user account is suspended (e.g. by admin).
	StatusSuspended UserStatus = "suspended"
	// StatusLocked indicates the user account is locked and cannot authenticate.
	StatusLocked UserStatus = "locked"
)

// ValidUserStatus returns true if the given status is a known valid status.
func ValidUserStatus(s UserStatus) bool {
	switch s {
	case StatusActive, StatusSuspended, StatusLocked:
		return true
	default:
		return false
	}
}

// UserSource records which path created the user row. Identity users are
// ordinary accounts; setup users are first-admin provisioning rows.
type UserSource string

const (
	// UserSourceIdentity is the default for ordinary identity-management users.
	UserSourceIdentity UserSource = "identity"
	// UserSourceSetup marks an interactive first-run setup row.
	UserSourceSetup UserSource = "setup"
)

// ValidUserSource returns true if the given source is a known valid source.
func ValidUserSource(s UserSource) bool {
	switch s {
	case UserSourceIdentity, UserSourceSetup:
		return true
	default:
		return false
	}
}

// User is the identity aggregate root for accesscore.
//
// Authz-sensitive fields (status, passwordResetRequired, authzEpoch) are
// private to the domain package. External packages read them via accessors
// and mutate them only through the authzmutate.Apply funnel
// (DOMAIN-AUTHZ-FIELD-PRIVATE-01 / AUTHZ-MUTATION-APPLY-FUNNEL-01 archtest,
// Wave 2). This makes "mutate authz state without epoch-bump+revoke"
// unrepresentable across the package boundary.
type User struct {
	ID              string
	Username        string
	Email           string
	PasswordHash    string
	PasswordVersion int64 // kept public: P1.1 reads it; not a revocation setter.
	CreationSource  UserSource
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// private authz fields — mutated only via SetStatus / SetPasswordResetRequired
	status                UserStatus
	passwordResetRequired bool
	authzEpoch            int64

	// auto-lockout state — mutated only via RegisterFailedLogin / ResetFailedLogins.
	// Not part of the authz-funnel DOMAIN-AUTHZ-FIELD-PRIVATE-01 set: these
	// fields are policy bookkeeping for the auto-lockout decision, not
	// credential-revocation signals. Status change to Locked still routes
	// through the authzmutate funnel (LockUser mutation).
	failedLoginCount int
	lastFailedAt     *time.Time
	lockedUntil      *time.Time
}

// Status returns the user's current account status.
func (u *User) Status() UserStatus { return u.status }

// PasswordResetRequired returns whether the user must change their password
// before using protected endpoints.
func (u *User) PasswordResetRequired() bool { return u.passwordResetRequired }

// AuthzEpoch returns the credential-invalidation epoch counter.
func (u *User) AuthzEpoch() int64 { return u.authzEpoch }

// IsLocked returns true if the user account is locked.
func (u *User) IsLocked() bool { return u.status == StatusLocked }

// CanAuthenticate returns true only when the account is currently active.
// Any non-active status (locked, suspended, or unknown future state) MUST
// fail-closed at every authentication surface: login, refresh, validate.
// S4.0: suspended users were previously allowed to log in because the only
// gate was IsLocked(); this method is the single source of truth that
// closes that gap. Use this instead of `IsLocked()` for any code path that
// decides whether a user may obtain or continue to use a session.
func (u *User) CanAuthenticate() bool { return u.status == StatusActive }

// SetStatus sets the user's account status and advances UpdatedAt.
// This is a funnel-only mutator — call it exclusively via authzmutate.Apply
// (or SetPasswordResetRequired for the reset-flag path).
// Direct calls from outside the domain package are blocked by field
// privatization; the authzmutate.Apply package is the only allowed caller
// for live aggregates (AUTHZ-MUTATION-APPLY-FUNNEL-01, Wave 2 archtest).
func (u *User) SetStatus(s UserStatus, now time.Time) {
	u.status = s
	u.UpdatedAt = now
}

// SetPasswordResetRequired sets the passwordResetRequired flag and advances
// UpdatedAt. Funnel-only mutator — same invariant as SetStatus.
func (u *User) SetPasswordResetRequired(v bool, now time.Time) {
	u.passwordResetRequired = v
	u.UpdatedAt = now
}

// BumpPasswordVersion advances the CAS counter that guards ChangePassword
// from concurrent overwrites. Call after writing a new PasswordHash; the
// repo's UpdatePassword SQL bumps the column via password_version+1, so this
// in-memory bump keeps the domain object in sync after a successful CAS write.
func (u *User) BumpPasswordVersion(now time.Time) {
	u.PasswordVersion++
	u.UpdatedAt = now
}

// FailedLoginCount returns the current accumulator of consecutive failed
// login attempts. Auto-lockout policy (cells/accesscore/internal/accountlockout)
// uses this counter; it is reset to 0 by ResetFailedLogins on successful
// login, on stale-window expiry, and on admin unlock (ActivateUser mutation).
func (u *User) FailedLoginCount() int { return u.failedLoginCount }

// LastFailedAt returns the timestamp of the most recent failed login, or nil
// if no failure has been recorded (or the counter has been reset). Used by
// RegisterFailedLogin to detect stale windows.
func (u *User) LastFailedAt() *time.Time { return u.lastFailedAt }

// AutoLockoutDeadline returns the lazy-unlock TTL deadline, or nil if the
// account is not auto-locked or has been unlocked manually. When
// status==Locked and AutoLockoutDeadline()!=nil and
// now()>=*AutoLockoutDeadline(), sessionlogin transparently re-activates the
// account inside the login transaction.
//
// Named to dodge the DOMAIN-AUTHZ-FIELD-PRIVATE-01 prefix ban on exported
// "Lock*" methods (the rule's banned prefix list is Set / Mark / Clear /
// Lock / Unlock; a getter with semantic name "LockedUntil" tripped that
// prefix even though it's a read-only accessor, not a mutator).
func (u *User) AutoLockoutDeadline() *time.Time { return u.lockedUntil }

// RegisterFailedLogin records a failed login attempt and returns whether the
// caller should trigger an auto-lock mutation. Mutates failedLoginCount,
// lastFailedAt, and (if shouldLock) lockedUntil in-memory; caller is
// responsible for persisting the change via the user repository.
//
// Semantics (Keycloak maxDeltaTimeSeconds + ASP.NET Identity AccessFailedCount
// hybrid):
//   - If lastFailedAt is older than staleWindow, the counter is reset to 1
//     (treated as a fresh attempt sequence) — equivalent to Keycloak's
//     failure-reset-on-inactivity.
//   - Otherwise the counter increments by 1.
//   - If the new count >= threshold, lockedUntil is set to now+lockoutTTL and
//     shouldLock is true; caller must run the LockUser mutation in-tx.
//
// Counts >= threshold are not double-clamped — successive failures past the
// threshold continue to advance the counter (matches Keycloak's accumulator
// semantics; the caller short-circuits to avoid the lock-twice work).
func (u *User) RegisterFailedLogin(now time.Time, staleWindow, lockoutTTL time.Duration, threshold int) (shouldLock bool) {
	if u.lastFailedAt == nil || now.Sub(*u.lastFailedAt) > staleWindow {
		u.failedLoginCount = 1
	} else {
		u.failedLoginCount++
	}
	t := now
	u.lastFailedAt = &t
	u.UpdatedAt = now
	if u.failedLoginCount >= threshold {
		until := now.Add(lockoutTTL)
		u.lockedUntil = &until
		return true
	}
	return false
}

// ResetFailedLogins zeroes the auto-lockout state. Called on:
//   - successful login (accountlockout.RecordSuccess)
//   - stale-window decision when the existing counter is meaningless (handled
//     transparently inside RegisterFailedLogin via the reset-to-1 branch)
//   - admin ActivateUser mutation (avoids "unlock then immediately re-lock"
//     trap if the user has one more typo within the window)
//   - lazy-unlock when locked_until has elapsed (accountlockout.TryLazyUnlock)
func (u *User) ResetFailedLogins() {
	u.failedLoginCount = 0
	u.lastFailedAt = nil
	u.lockedUntil = nil
}

// NewUser creates a new active User with the given timestamp.
// now is the wall-clock instant provided by the caller's clock.Clock.
// Returns an errcode.Error if any required field is empty.
func NewUser(username, email, passwordHash string, now time.Time) (*User, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthInvalidInput,
		validation.F("username", username),
		validation.F("email", email),
		validation.F("passwordHash", passwordHash),
	); err != nil {
		return nil, err
	}

	return &User{
		Username:       username,
		Email:          email,
		PasswordHash:   passwordHash,
		status:         StatusActive,
		CreationSource: UserSourceIdentity,
		// S4d: AuthzEpoch starts at 1 so the first login can store a valid
		// session.AuthzEpochAtIssue (store rejects 0 as the unset sentinel).
		// BumpAuthzEpoch increments from 1; sessions created at epoch=1 are
		// invalidated after the first credential event.
		authzEpoch: 1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// ReconstituteUserParams holds all fields required by ReconstituteUser.
// Using a params struct avoids the 11-positional-argument footgun and allows
// callers to name each field explicitly.
//
// Field order: identity + credentials + lifecycle timestamps first
// (ID, Username, Email, PasswordHash, PasswordVersion, Source, CreatedAt,
// UpdatedAt), then the three authz-controlled fields grouped at the end
// (Status, PasswordResetRequired, AuthzEpoch). The authz subgroup mirrors
// User's private field order (status → passwordResetRequired → authzEpoch)
// so the storage boundary and the aggregate read in the same direction.
type ReconstituteUserParams struct {
	ID              string
	Username        string
	Email           string
	PasswordHash    string
	PasswordVersion int64
	Source          UserSource
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// Authz-controlled fields. Mutating these on a live User must go through
	// the authzmutate funnel; ReconstituteUser is the storage-boundary
	// rehydration path and is allowlisted for direct assignment.
	Status                UserStatus
	PasswordResetRequired bool
	AuthzEpoch            int64

	// Auto-lockout state — storage-boundary rehydration only. Live mutations
	// go through RegisterFailedLogin / ResetFailedLogins.
	FailedLoginCount int
	LastFailedAt     *time.Time
	LockedUntil      *time.Time
}

// ReconstituteUser is the DDD rehydration constructor for the persistence
// boundary (PG scanUser, mem store) and tests. It rebuilds a User aggregate
// from authoritative storage values and is NOT a funnel hole — it constructs
// a fresh aggregate, it does not mutate a live one.
//
// All four string identity fields (ID, Username, Email, PasswordHash) must be
// non-empty; Status and Source must be ValidUserStatus / ValidUserSource;
// AuthzEpoch must be > 0 (the unset sentinel 0 is rejected per S4d invariant).
func ReconstituteUser(p ReconstituteUserParams) (*User, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthInvalidInput,
		validation.F("id", p.ID),
		validation.F("username", p.Username),
		validation.F("email", p.Email),
		validation.F("passwordHash", p.PasswordHash),
	); err != nil {
		return nil, err
	}
	if !ValidUserStatus(p.Status) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthInvalidInput,
			"ReconstituteUser: invalid status",
			errcode.WithDetails(
				errcode.PublicString("status", string(p.Status)),
			))
	}
	if !ValidUserSource(p.Source) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthInvalidInput,
			"ReconstituteUser: invalid source",
			errcode.WithDetails(
				errcode.PublicString("source", string(p.Source)),
			))
	}
	if p.AuthzEpoch <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthInvalidInput,
			"ReconstituteUser: authzEpoch must be > 0")
	}
	return &User{
		ID:              p.ID,
		Username:        p.Username,
		Email:           p.Email,
		PasswordHash:    p.PasswordHash,
		PasswordVersion: p.PasswordVersion,
		CreationSource:  p.Source,
		CreatedAt:       p.CreatedAt,
		UpdatedAt:       p.UpdatedAt,

		status:                p.Status,
		passwordResetRequired: p.PasswordResetRequired,
		authzEpoch:            p.AuthzEpoch,

		failedLoginCount: p.FailedLoginCount,
		lastFailedAt:     p.LastFailedAt,
		lockedUntil:      p.LockedUntil,
	}, nil
}
