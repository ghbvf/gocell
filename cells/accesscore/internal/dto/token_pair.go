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

// ToTokenPairResponse builds the wire DTO from the service-layer model.
// The mapping is explicit so future fields on TokenPair don't auto-leak to
// the wire and so the boundary is grep-able from the call site.
//
// The explicit struct literal is intentional: it creates a visible wire/model
// boundary that prevents accidental field auto-propagation when either struct
// evolves independently. Staticcheck S1016 (prefer type conversion) is
// suppressed on the return line because the value-cast is exactly what we want
// to avoid.
func ToTokenPairResponse(p TokenPair) TokenPairResponse {
	return TokenPairResponse{ //nolint:staticcheck // S1016: explicit field mapping is intentional — prevents accidental wire/model field coupling
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt,
		SessionID:             p.SessionID,
		UserID:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}
