package domain

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Note: Session methods that need a wall-clock value accept a now time.Time
// parameter so callers supply the time via an injected clock.Clock rather than
// calling time.Now() directly. This keeps the domain free of framework deps.

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
// now is the wall-clock instant provided by the caller's clock.Clock.
// Returns an errcode.Error if any required field is empty.
func NewSession(userID, accessToken string, expiresAt time.Time, now time.Time) (*Session, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthSessionInvalidInput,
		validation.F("userID", userID),
		validation.F("accessToken", accessToken),
	); err != nil {
		return nil, err
	}

	return &Session{
		UserID:      userID,
		AccessToken: accessToken,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
		Version:     1,
	}, nil
}

// Revoke marks the session as revoked at the given time.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (s *Session) Revoke(now time.Time) {
	s.RevokedAt = &now
}

// IsRevoked returns true if the session has been revoked.
func (s *Session) IsRevoked() bool {
	return s.RevokedAt != nil
}

// IsExpired returns true if the session's expiry time has passed.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (s *Session) IsExpired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}
