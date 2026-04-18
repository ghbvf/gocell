// Package dto provides handler-level response types for access-core,
// shared across slices that return the same entity shape.
package dto

import "time"

// TokenPair is the service-layer model for an issued token pair. It is shared
// across slices within access-core (session-login, identity-manage) via this
// internal/dto package to avoid cross-slice imports.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	SessionID             string
	PasswordResetRequired bool
}

// TokenPairResponse is the public DTO for token pairs, isolating the API
// contract from the service-layer model. Shared by sessionlogin, sessionrefresh,
// and identitymanage slices (same cell, multi-slice → internal/dto/ per DTO scope rule).
type TokenPairResponse struct {
	AccessToken           string    `json:"accessToken"`
	RefreshToken          string    `json:"refreshToken"`
	ExpiresAt             time.Time `json:"expiresAt"`
	SessionID             string    `json:"sessionId,omitempty"`
	PasswordResetRequired bool      `json:"passwordResetRequired"`
}
