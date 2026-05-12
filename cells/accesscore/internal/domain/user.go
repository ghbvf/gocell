// Package domain contains the accesscore Cell domain models.
package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// BcryptCost is the shared bcrypt work factor for password hashing across
// the accesscore cell. All password hashing call sites (seed admin, user
// creation) MUST use this constant for consistency.
//
// ref: Ory Kratos BcryptDefaultCost=12; OWASP 2023 minimum recommendation.
const BcryptCost = 12

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
type User struct {
	ID                    string
	Username              string
	Email                 string
	PasswordHash          string
	PasswordVersion       int64
	PasswordResetRequired bool
	Status                UserStatus
	CreationSource        UserSource
	CreatedAt             time.Time
	UpdatedAt             time.Time
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
		Status:         StatusActive,
		CreationSource: UserSourceIdentity,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// MarkPasswordResetRequired sets the PasswordResetRequired flag to true and
// advances UpdatedAt. Call this when creating an admin-bootstrap user that
// must change its password on first login.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (u *User) MarkPasswordResetRequired(now time.Time) {
	u.PasswordResetRequired = true
	u.UpdatedAt = now
}

// ClearPasswordResetRequired unsets the PasswordResetRequired flag and
// advances UpdatedAt. Call this after the user has successfully changed their
// password.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (u *User) ClearPasswordResetRequired(now time.Time) {
	u.PasswordResetRequired = false
	u.UpdatedAt = now
}

// LockAccount sets the user status to locked.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (u *User) LockAccount(now time.Time) {
	u.Status = StatusLocked
	u.UpdatedAt = now
}

// UnlockAccount sets the user status to active.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (u *User) UnlockAccount(now time.Time) {
	u.Status = StatusActive
	u.UpdatedAt = now
}

// IsLocked returns true if the user account is locked.
func (u *User) IsLocked() bool {
	return u.Status == StatusLocked
}

// CanAuthenticate returns true only when the account is currently active.
// Any non-active status (locked, suspended, or unknown future state) MUST
// fail-closed at every authentication surface: login, refresh, validate.
// S4.0: suspended users were previously allowed to log in because the only
// gate was IsLocked(); this method is the single source of truth that
// closes that gap. Use this instead of `IsLocked()` for any code path that
// decides whether a user may obtain or continue to use a session.
func (u *User) CanAuthenticate() bool {
	return u.Status == StatusActive
}

// BumpPasswordVersion advances the CAS counter that guards ChangePassword
// from concurrent overwrites. Call after writing a new PasswordHash; the
// repo's UpdatePassword SQL bumps the column via password_version+1, so this
// in-memory bump keeps the domain object in sync after a successful CAS write.
func (u *User) BumpPasswordVersion(now time.Time) {
	u.PasswordVersion++
	u.UpdatedAt = now
}
