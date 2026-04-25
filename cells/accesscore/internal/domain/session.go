package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Session represents an authenticated user session with its access token and
// expiry. Refresh tokens live in runtime/auth/refresh.Store (append-only
// lineage per migration 012) and are not mirrored on Session.
type Session struct {
	ID          string
	UserID      string
	AccessToken string
	ExpiresAt   time.Time
	RevokedAt   *time.Time // nil = not revoked
	CreatedAt   time.Time
	Version     int64 // optimistic lock version; incremented on each update
}

// NewSession creates a new session for the given user.
// Returns an errcode.Error if any required field is empty.
func NewSession(userID, accessToken string, expiresAt time.Time) (*Session, error) {
	if userID == "" {
		return nil, errcode.New(errcode.ErrAuthSessionInvalidInput, "userID is required")
	}
	if accessToken == "" {
		return nil, errcode.New(errcode.ErrAuthSessionInvalidInput, "accessToken is required")
	}

	return &Session{
		UserID:      userID,
		AccessToken: accessToken,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
		Version:     1,
	}, nil
}

// Revoke marks the session as revoked at the current time.
func (s *Session) Revoke() {
	now := time.Now()
	s.RevokedAt = &now
}

// IsRevoked returns true if the session has been revoked.
func (s *Session) IsRevoked() bool {
	return s.RevokedAt != nil
}

// IsExpired returns true if the session's expiry time has passed.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}
