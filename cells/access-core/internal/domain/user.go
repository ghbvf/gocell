// Package domain contains the access-core Cell domain models.
package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)


// BcryptCost is the shared bcrypt work factor for password hashing across
// the access-core cell. All password hashing call sites (seed admin, user
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

// User is the identity aggregate root for access-core.
type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Status       UserStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewUser creates a new active User with the current timestamp.
// Returns an errcode.Error if any required field is empty.
func NewUser(username, email, passwordHash string) (*User, error) {
	if username == "" {
		return nil, errcode.New(errcode.ErrAuthInvalidInput, "username is required")
	}
	if email == "" {
		return nil, errcode.New(errcode.ErrAuthInvalidInput, "email is required")
	}
	if passwordHash == "" {
		return nil, errcode.New(errcode.ErrAuthInvalidInput, "passwordHash is required")
	}

	now := time.Now()
	return &User{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		Status:       StatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Lock sets the user status to locked.
func (u *User) Lock() {
	u.Status = StatusLocked
	u.UpdatedAt = time.Now()
}

// Unlock sets the user status to active.
func (u *User) Unlock() {
	u.Status = StatusActive
	u.UpdatedAt = time.Now()
}

// IsLocked returns true if the user account is locked.
func (u *User) IsLocked() bool {
	return u.Status == StatusLocked
}
