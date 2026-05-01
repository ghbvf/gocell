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
// ordinary accounts; setup/bootstrap users are first-admin provisioning rows.
type UserSource string

const (
	// UserSourceIdentity is the default for ordinary identity-management users.
	UserSourceIdentity UserSource = "identity"
	// UserSourceSetup marks an interactive first-run setup row.
	UserSourceSetup UserSource = "setup"
	// UserSourceBootstrap marks a headless initial-admin bootstrap row.
	UserSourceBootstrap UserSource = "bootstrap"
)

// ValidAdminProvisionSource returns true for sources owned by admin provisioning.
func ValidAdminProvisionSource(s UserSource) bool {
	switch s {
	case UserSourceSetup, UserSourceBootstrap:
		return true
	default:
		return false
	}
}

// ProvisionState tracks whether a provisioning-owned user row is still safe to
// recover. Only pending rows from the same source may be reclaimed.
type ProvisionState string

const (
	// ProvisionStateNone means the user was not created by admin provisioning.
	ProvisionStateNone ProvisionState = ""
	// ProvisionStatePending means UserRepo.Create succeeded but first-admin
	// provisioning has not completed all required writes yet.
	ProvisionStatePending ProvisionState = "pending"
	// ProvisionStateComplete means the provisioning-owned row was completed.
	ProvisionStateComplete ProvisionState = "complete"
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
	ProvisionState        ProvisionState
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// NewUser creates a new active User with the current timestamp.
// Returns an errcode.Error if any required field is empty.
func NewUser(username, email, passwordHash string) (*User, error) {
	if err := validation.RequireNotBlank(errcode.ErrAuthInvalidInput,
		validation.F("username", username),
		validation.F("email", email),
		validation.F("passwordHash", passwordHash),
	); err != nil {
		return nil, err
	}

	now := time.Now()
	return &User{
		Username:       username,
		Email:          email,
		PasswordHash:   passwordHash,
		Status:         StatusActive,
		CreationSource: UserSourceIdentity,
		ProvisionState: ProvisionStateNone,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// MarkProvisionPending marks a setup/bootstrap-owned row as recoverable until
// the first-admin provisioning sequence completes.
func (u *User) MarkProvisionPending(source UserSource) {
	u.CreationSource = source
	u.ProvisionState = ProvisionStatePending
	u.UpdatedAt = time.Now()
}

// MarkProvisionComplete marks a setup/bootstrap-owned row as no longer
// recoverable by duplicate-username recovery.
func (u *User) MarkProvisionComplete() {
	u.ProvisionState = ProvisionStateComplete
	u.UpdatedAt = time.Now()
}

// IsRecoverableProvisionOrphan verifies that a duplicate username belongs to
// the same interrupted provisioning attempt class, not to an ordinary user.
func (u *User) IsRecoverableProvisionOrphan(source UserSource) bool {
	return ValidAdminProvisionSource(source) &&
		u.CreationSource == source &&
		u.ProvisionState == ProvisionStatePending
}

// MarkPasswordResetRequired sets the PasswordResetRequired flag to true and
// advances UpdatedAt. Call this when creating an admin-bootstrap user that
// must change its password on first login.
func (u *User) MarkPasswordResetRequired() {
	u.PasswordResetRequired = true
	u.UpdatedAt = time.Now()
}

// ClearPasswordResetRequired unsets the PasswordResetRequired flag and
// advances UpdatedAt. Call this after the user has successfully changed their
// password.
func (u *User) ClearPasswordResetRequired() {
	u.PasswordResetRequired = false
	u.UpdatedAt = time.Now()
}

// LockAccount sets the user status to locked.
func (u *User) LockAccount() {
	u.Status = StatusLocked
	u.UpdatedAt = time.Now()
}

// UnlockAccount sets the user status to active.
func (u *User) UnlockAccount() {
	u.Status = StatusActive
	u.UpdatedAt = time.Now()
}

// IsLocked returns true if the user account is locked.
func (u *User) IsLocked() bool {
	return u.Status == StatusLocked
}
