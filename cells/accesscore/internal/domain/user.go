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

// User is the identity aggregate root for accesscore.
type User struct {
	ID                    string
	Username              string
	Email                 string
	PasswordHash          string
	PasswordResetRequired bool
	Status                UserStatus
	CreationSource        UserSource
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// Version is the optimistic-concurrency fencing token (ref: K8s apimachinery
	// resourceVersion). Incremented by the repo on every successful write.
	// Migration 022 initializes this column to 1 for all existing rows.
	Version int64
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
		Version:        1,
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
