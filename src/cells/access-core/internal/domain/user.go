// Package domain contains the access-core Cell domain models.
package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Error codes for access-core domain.
const (
	ErrUserInvalidInput errcode.Code = "ERR_AUTH_INVALID_INPUT"
	ErrUserLocked       errcode.Code = "ERR_AUTH_USER_LOCKED"
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
		return nil, errcode.New(ErrUserInvalidInput, "username is required")
	}
	if email == "" {
		return nil, errcode.New(ErrUserInvalidInput, "email is required")
	}
	if passwordHash == "" {
		return nil, errcode.New(ErrUserInvalidInput, "passwordHash is required")
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
