// Package dto provides handler-level response types for accesscore,
// shared across slices that return the same entity shape.
package dto

import "time"

// TokenPair is the service-layer model for an issued token pair, shared by
// every accesscore slice that mints tokens (sessionlogin / sessionrefresh /
// identitymanage). All fields are populated on every success path.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	SessionID             string
	UserID                string
	PasswordResetRequired bool
}

// TokenPairResponse is the wire shape returned by every token-issuing
// endpoint. JSON shape is stable across login / refresh / change-password.
// Use ToTokenPairResponse to convert from the service-layer model.
type TokenPairResponse struct {
	AccessToken           string    `json:"accessToken"`
	RefreshToken          string    `json:"refreshToken"`
	ExpiresAt             time.Time `json:"expiresAt"`
	SessionID             string    `json:"sessionId"`
	UserID                string    `json:"userId"`
	PasswordResetRequired bool      `json:"passwordResetRequired"`
}

// ToTokenPairResponse converts the service-layer model to the wire DTO.
func ToTokenPairResponse(p TokenPair) TokenPairResponse {
	return TokenPairResponse(p)
}
